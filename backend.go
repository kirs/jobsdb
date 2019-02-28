package main

import (
	"cloud.google.com/go/pubsub"
	"context"
	"fmt"
	"github.com/Shopify/jobsdb/types"
	"github.com/golang/protobuf/proto"
	"github.com/sirupsen/logrus"
	"log"
)

// Jobsdb uses memory to store inflight state like locks and concurrency
// but for the queue backlog it requires an actual backend.
// Below is the list of operations that the backend must support.
// Google pubsub is the most straightforward option for that backend,
// though it's not impossible to implement same features with Redis.

type backend interface {
	Ack(*inflightJob)
	Nack(*inflightJob)
	Receive(context.Context, chan *inflightJob) error
	Publish(context.Context, *types.QueuedJob) error
	Stop()
}

type pubsubBackend struct {
	topic  *pubsub.Topic
	client *pubsub.Client
}

func newPubsubBackend(ctx context.Context, projectID string, topicName string) *pubsubBackend {
	pubsubClient, err := pubsub.NewClient(ctx, projectID)
	if err != nil {
		log.Fatalf("Failed to create client: %v\n", err)
	}

	pubsubTopic, err := pubsubClient.CreateTopic(ctx, topicName)
	if err != nil {
		log.Printf("Failed to create topic: %v\n", err)
		pubsubTopic = pubsubClient.Topic(topicName)
	}
	defer pubsubTopic.Stop()

	return &pubsubBackend{topic: pubsubTopic, client: pubsubClient}
}

func (b *pubsubBackend) Stop() {
	b.topic.Stop()
}

func (b *pubsubBackend) Ack(job *inflightJob) {
	job.m.Ack()
}

func (b *pubsubBackend) Nack(job *inflightJob) {
	job.m.Nack()
}

func (b *pubsubBackend) Receive(ctx context.Context, buffer chan *inflightJob) error {
	sub, err := b.client.CreateSubscription(ctx, fmt.Sprintf("sub-name"), pubsub.SubscriptionConfig{Topic: b.topic})
	if err != nil {
		fmt.Printf("count not create subscription: %s\n", err)
		sub = b.client.Subscription(fmt.Sprintf("sub-name"))
	}

	return sub.Receive(ctx, func(ctx context.Context, m *pubsub.Message) {
		var queuedJob types.QueuedJob
		err := proto.Unmarshal(m.Data, &queuedJob)
		if err != nil {
			logrus.WithError(err).Warn("Failed to unmarshal the job")
			m.Ack()
			return
		}
		buffer <- &inflightJob{m: m, job: queuedJob.Job, concurrency: queuedJob.Concurrency}
	})
}

func (b *pubsubBackend) Publish(ctx context.Context, job *types.QueuedJob) error {
	encoded, err := proto.Marshal(job)

	if err != nil {
		logrus.WithError(err).Warn("Failed to marshal the job")
		return err
	}

	b.topic.Publish(ctx, &pubsub.Message{Data: encoded})
	return nil
}
