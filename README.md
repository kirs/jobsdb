# Jobsdb

***Experimental code - don't use in production!***

Meet *jobsdb*, a service on top of Google pub/sub (or any other queue with a similar interface) that acts as a gRPC server to dispatch jobs to application workers. It's inspired by CPU scheduling algorithms for fair prioritization of jobs and implements common features like concurrency control and job uniqueness.

## What is jobsdb trying to solve?

* **Control plane vs data plane**. In a typical Rails app, workers would talk directly to the datastore with queues. With thousands of workers it would mean quite a lot of connections to that datastore, and repetitive checks that each worker has to perform. Jobsdb acts as a **control plane service**, moves that logic from application workers and does all shared work for them.
* **Ambiguity of queues**. In a typical app there would be a  dozen of domain-specific queues. `payments` queue would be more important than `webhooks`, and `reindexing` would be less important than `webhooks` but not `default`. This is confusing for developers when it comes to choosing a queue, and they would often want to have their own queue. **Inspired by CPU scheduling algorithms, jobsdb is using [Priority-based scheduling with aging](https://www.geeksforgeeks.org/starvation-aging-operating-systems/)**. This allows to express job priority as a number instead of domain-specific queue, and give more control over priority of jobs that share workers.
* There's a variety of distributed databases that provide the queue primitive (Kafka, Google pubsub, Amazon SQS), but for a feature-rich jobs framework there's a lot more primitives needed. **Jobsdb uses a queue-backed store for the backlog of jobs and implements in-memory store for the rest of the state**, like concurrency control and job uniqueness.
* A lot of state synchronizations for features like concurrency and uniqueness are faster to be done in-memory instead of, for instance, sharing a Redis between workers. Since there's no shared state, there's a limitation of having a single instance of jobsdb per a shard/partition of your app.
* **Middlewares**. Running in a control plane, it's easier to enforce rules that would otherwise have to be implemented inside workers itself. The variety of rules is large and all of them come from a real life use cases at Shopify:
	* "transfer the job to another region if current region became passive"
	* "ignore (blackhole) jobs that match specific pattern during an incident"
	* "artificially de-prioritize jobs of a violating tenant (shed the load)"
	* "quarantine similar jobs that have never been ack-ed to avoid starving workers"

Same rules are possible to implement without jobsdb, with a Resque/Sidekiq middleware, but it's a lot more efficient in terms of resources to have them at the control plane and leave workers free from that logic.

## What is jobsdb NOT trying to solve?

* Jobsdb is not trying to use the same datastore like Redis for all primitives (queues, k/v access with TTL, sorted sets etc). Instead, it's using an external storage for the jobs backlog, and keeps everything else in-memory.
* Jobsdb is not trying to leave job framework a queue-only backend and gives rich concurrency control as a first class citizen feature.
* Jobsdb is not providing job batches feature which is hard to track and sync the state.
* Jobsdb is not a truly distributed storage for jobs. The nature of jobs queue is that durability and availability are often traded for *throughput*, and getting a consensus of workers

## What are the weakest design points?

* By rejecting the idea of a shared store for concurrency metadata, we're saying no to horizontal scalability and limit ourselves to a single process model. Current hypothesis is that Go's concurrency model is enough to compensate for that by extensively using many cores and goroutines, but this might become a growing problem for workloads that don't allow to run a jobsdb instance per app's shard or partition.  

## Features

Apart from enqueue/reserve/ack operations, jobsdb gives an extensive control over the job concurrency, adopted from existing primitives that we've been using at Shopify. Some examples:

```
ConcurrencyControl.new(limit: 1, ordered: false, key: "tenant_id_1") # allow only one job at once for given key, ignore the mix of order

ConcurrencyControl.new(limit: 1, ordered: true, key: "tenant_id_1") # allow only one job at once for given key, preserve the order (similar to Shopify's LockQueue)

ConcurrencyControl.new(limit: 1, ordered: false, key: "tenant_id_1", on_limit: DROP) # allow only one job at once for given key, ignore any other enqueued jobs while the first one is running (similar to Shopify's Locking feature)

ConcurrencyControl.new(limit: 600, rate_per_window: 300, ordered: false, key: "tenant_id_1") # allow 600 jobs at once for given key, but no more than 300 jobs per second in case those jobs complete really fast
```

As a protocol to talk to workers, jobsdb is using **gRPC**.

* gRPC has been chosen because of low overhead, which is thinner compared to the GraphQL stack
* gRPC is using http2 and keep-alive connections, which is great for reusing connections on the client
* gRPC has a first class Go support with ability to generate a Ruby client with a single command

## What's missing?

* A very simple version of priority-based scheduling is in place, but there's no [aging](https://www.geeksforgeeks.org/starvation-aging-operating-systems/) implemented yet.
* Detection of dead workers (if the job was reserved but never acknowledged)
* State persistence for zero-downtime rollout. Jobsdb keeps all state in memory, so if we want to deploy it, we gotta reset the state. It would be nice to be able to dump the current state somewhere so the new revision of jobsdb can pick it up.
* `rate_per_window` concurrency control.
* Production benchmarks. I've developed the project with local "fake" pubsub, and I've only load-tested it on a dual core MacBook Pro, which is not very representative. Jobsdb should have an extensive *production* benchmark.
* High Availability. It's unclear yet what's the best way to do that with in-memory store. Could be a number of replicas all waiting to acquire a mutually exclusive lock?

## Getting started

1. Run a local version of Google pubsub:
    * `gcloud beta emulators pubsub start`
    * `gcloud beta emulators pubsub env-init`

2. Start jobsdb: `script/start --verbose` (the flag is optional)

3. Enqueue and perform some jobs with the client:
    * `cd ruby`
    * `ruby client.rb`