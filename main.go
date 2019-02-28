//go:generate protoc -I types --go_out=plugins=grpc:types types/types.proto

package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/golang/protobuf/ptypes/timestamp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/sirupsen/logrus"
	"net/http"

	"log"
	"math/rand"
	"net"
	"sync"
	"time"

	"cloud.google.com/go/pubsub"
	"github.com/Shopify/jobsdb/types"
	"github.com/golang/protobuf/ptypes/empty"
	"github.com/nu7hatch/gouuid"
	"google.golang.org/grpc"
	_ "net/http/pprof"
)

var (
	jobsEnqueuedCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jobs_enqueued_total",
		Help: "The total number of jobs enqueued",
	})
	jobsConcurrencyLimitedCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jobs_concurrency_limit",
		Help: "",
	})
	jobsDispatchedCounter = promauto.NewCounter(prometheus.CounterOpts{
		Name: "jobs_dispatched",
		Help: "",
	})
	jobsInflightGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "jobs_inflight",
		Help: "",
	})
	grpcPort = 3001
)

type inflightJob struct {
	job         *types.Job
	m           *pubsub.Message
	concurrency *types.ConcurrencyControl
}

type jobListNode struct {
	Job  *inflightJob
	Next *jobListNode
}

type actor struct {
	job        *types.Job
	acquiredAt time.Time
}

type concurrencyState struct {
	semaphore *concurrencyLimiter
}

type state struct {
	m           sync.Mutex
	inflight    sync.Map
	concurrency map[string]*concurrencyState
}

func linkedListLen(topJob *jobListNode) int {
	count := 0
	for {
		count++
		if topJob.Next == nil {
			break
		}
		topJob = topJob.Next
	}
	return count
}

func pickJobsFromHeap(topJob *jobListNode) (*jobListNode, *jobListNode) {
	totalLen := linkedListLen(topJob)
	// fmt.Printf("total len: %d; %d\n", totalLen, totalLen/2)
	currentJob := topJob
	i := 0
	for {
		// TODO: do not pass for submission if its concurrency is at limit
		if i >= (totalLen / 2) { // take half
			resubmission := currentJob.Next
			currentJob.Next = nil
			return topJob, resubmission
		}

		currentJob = currentJob.Next
		i++
	}
}

func batchJobs(source chan *inflightJob, limit time.Duration) *jobListNode {
	var head *jobListNode
	var prev *jobListNode
	var current *jobListNode
	n := 0
	buffering := true
	for buffering {
		if n >= 10 {
			buffering = false
			continue
		}
		select {
		case job := <-source:
			current = &jobListNode{Job: job}
			if head == nil {
				head = current
			}

			if prev != nil {
				prev.Next = current
			}
			prev = current
			n++
		case <-time.NewTimer(limit).C:
			buffering = false
			continue
		}
	}
	if n > 0 {
		logrus.WithField("batch_size", n).Debug("Batched jobs")
	}
	return head
}

func dispatchJobs(server *jobsServer) {
	var state *state = server.state
	var jobs chan *inflightJob = server.reserve

	buffer := make(chan *inflightJob)

	go func(source chan *inflightJob, target chan *inflightJob) {
		for {
			head := batchJobs(source, time.Second*3)

			// pure priority scheduling without aging, sorted by priority
			current := MergeSort(head)
			for {
				if current == nil {
					break
				}
				if current.Job == nil {
					panic("current job should not be nil")
				}

				if current.Job.concurrency != nil {
					state.m.Lock()
					c, found := state.concurrency[current.Job.concurrency.Key]
					if !found {
						c = &concurrencyState{}
						c.semaphore = NewConcurrencyLimiter(int(current.Job.concurrency.MaxConcurrency))
						state.concurrency[current.Job.concurrency.Key] = c
					}
					if int32(c.semaphore.Limit()) != current.Job.concurrency.MaxConcurrency {
						c.semaphore.SetLimit(int(current.Job.concurrency.MaxConcurrency))
					}
					state.m.Unlock()
					if c.semaphore.TryAcquire(&actor{current.Job.job, time.Now()}) {
						jobsDispatchedCounter.Inc()
						target <- current.Job
					} else {
						jobsConcurrencyLimitedCounter.Inc()
						if current.Job.concurrency.OnLimit == types.ConcurrencyControl_DROP {
							//	droppig the job
						} else {
							// concurrency busy, retry later
							server.backend.Nack(current.Job)
							//current.Job.m.Nack()
						}
					}
				} else {
					jobsDispatchedCounter.Inc()
					target <- current.Job
				}

				current = current.Next
			}
		}
	}(buffer, jobs)

	err := server.backend.Receive(context.Background(), buffer)
	if err != nil {
		panic(err)
	}
}

type jobsServer struct {
	reserve chan *inflightJob
	state   *state
	backend backend
}

func newJobFromJobEnqueueRequest(job *types.JobEnqueueRequest) (*types.Job, error) {
	u4, err := uuid.NewV4()
	if err != nil {
		return nil, err
	}

	created := &types.Job{
		Priority: job.Priority,
		Id:       &types.JobId{Id: u4.String()},
		Payload:  job.Payload,
	}
	return created, nil
}

func (s *jobsServer) EnqueueJob(ctx context.Context, enqueueRequest *types.JobEnqueueRequest) (*empty.Empty, error) {
	if enqueueRequest.Concurrency != nil {
		_, found := s.state.concurrency[enqueueRequest.Concurrency.Key]
		// aka BackgroundQueue::Locking
		if found && enqueueRequest.Concurrency.MaxConcurrency == 1 && enqueueRequest.Concurrency.OnLimit == types.ConcurrencyControl_DROP {
			logrus.WithField("concurrency_key", enqueueRequest.Concurrency.Key).Info("Dropping the job because the concurrency key is already taken and on_limit is set to drop")
			return &empty.Empty{}, nil
		}
	}

	queuedJob := &types.QueuedJob{}

	job, err := newJobFromJobEnqueueRequest(enqueueRequest)
	if err != nil {
		log.Fatal(err)
		return nil, err
	}
	queuedJob.Job = job
	queuedJob.Concurrency = enqueueRequest.Concurrency
	queuedJob.EnqueuedAt = &timestamp.Timestamp{Seconds: time.Now().Unix()}

	s.backend.Publish(ctx, queuedJob)
	jobsEnqueuedCounter.Inc()

	var concurrencyKey string
	if queuedJob.Concurrency != nil {
		concurrencyKey = queuedJob.Concurrency.Key
	}
	logrus.WithField("priority", job.Priority).WithField("concurrency_key", concurrencyKey).Debug("Enqueued a job")

	jobsInflightGauge.Inc()
	return &empty.Empty{}, nil
}

func (s *jobsServer) ReserveJob(ctx context.Context, _ *empty.Empty) (*types.ReserveResult, error) {
	select {
	case job := <-s.reserve:
		_, loaded := s.state.inflight.LoadOrStore(job.job.Id.Id, job)
		if loaded {
			logrus.WithField("job_id", job.job.Id.Id).Error("Job with this job_id is already being processed")
			return &types.ReserveResult{Result: types.ReserveResult_NONE}, nil
		}
		jobsInflightGauge.Dec()
		return &types.ReserveResult{Result: types.ReserveResult_RESERVED, Job: job.job}, nil
	default:
		logrus.Debug("No job to reserve")
		return &types.ReserveResult{Result: types.ReserveResult_NONE}, nil
	}
}

func (s *jobsServer) AckJob(ctx context.Context, jobId *types.JobId) (*empty.Empty, error) {
	loaded, found := s.state.inflight.Load(jobId.Id)

	if found {
		// part of acknowledging a processed job is releasing its locks
		inflight := loaded.(*inflightJob)
		if inflight.concurrency != nil {
			s.state.m.Lock()
			c := s.state.concurrency[inflight.concurrency.Key]
			if c != nil {
				c.semaphore.ReleaseJob(inflight.job.Id.Id)
			}
			s.state.m.Unlock()
		}
		s.backend.Ack(inflight)
		logrus.WithField("job_id", jobId.Id).Debug("Acknowledged job")
	} else {
		s.state.m.Unlock()
		logrus.WithField("job_id", jobId.Id).Info("Could not find running job to acknowledge")
		return nil, fmt.Errorf("could not find running job for this worker to acknowledge")
	}

	return &empty.Empty{}, nil
}

var verboseMode = false

func init() {
	flag.BoolVar(&verboseMode, "verbose", false, "used to enable verbose mode")
}

func main() {
	flag.Parse()

	rand.Seed(time.Now().UnixNano())

	ctx := context.Background()

	if verboseMode {
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debug("Enabled verbose mode")
	} else {
		logrus.SetLevel(logrus.InfoLevel)
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", grpcPort))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()

	server := &jobsServer{}
	server.reserve = make(chan *inflightJob, 256) // capacity should be roughly equal to number of workers
	server.backend = newPubsubBackend(ctx, "YOUR_PROJECT_ID", "my-topic")
	server.state = &state{}
	server.state.concurrency = make(map[string]*concurrencyState)

	go dispatchJobs(server)

	go func() {
		http.Handle("/metrics", promhttp.Handler())
		http.ListenAndServe(":2112", nil)
	}()

	types.RegisterJobsServiceServer(grpcServer, server)
	logrus.WithField("port", grpcPort).Info("Listening to gRPC requests")
	grpcServer.Serve(lis)
}
