#!/usr/bin/env ruby

require 'bundler/setup'

this_dir = File.expand_path(File.dirname(__FILE__))
lib_dir = File.join(this_dir, 'lib')
$LOAD_PATH.unshift(lib_dir) unless $LOAD_PATH.include?(lib_dir)

require 'grpc'
require 'types_services_pb'
require 'securerandom'
require 'json'
require 'redis'

def enqueue_job(s, i, key)
  job = Types::JobEnqueueRequest.new(
    priority: i, payload: { shop_id: 1 }.to_json,
    # concurrency: Types::JobConcurrencySetting.new(
    #   key: key,
    #   max_concurrency: 1
    # )
  )
  s.enqueue_job(job)
  # puts "Enqueued #{job}: #{job.priority}"
end
require 'benchmark/ips'

Benchmark.ips do |x|
  # Configure the number of seconds used during
  # the warmup phase (default 2) and calculation phase (default 5)
  x.config(:time => 5, :warmup => 2)

  # These parameters can also be configured this way


  s = Types::JobsService::Stub.new('localhost:3001', :this_channel_is_insecure)



  r = Redis.new
  x.report("redis") {
      r.lpush("queue", {priority: 1, payload: { shop_id: 1 }.to_json}.to_json)
    }

    # Typical mode, runs the block as many times as it can
      x.report("jobsdb") {
        enqueue_job(s, 1, "")
      }
end
