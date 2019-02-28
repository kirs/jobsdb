#!/usr/bin/env ruby

require 'bundler/setup'

this_dir = File.expand_path(File.dirname(__FILE__))
lib_dir = File.join(this_dir, 'lib')
$LOAD_PATH.unshift(lib_dir) unless $LOAD_PATH.include?(lib_dir)

require 'grpc'
require 'types_services_pb'
require 'securerandom'
require 'json'

trap("INT") do
  $managed_pids.each do |pid|
    Process.kill("HUP", pid)
  end
  Process.wait
  exit
end

def main
  $managed_pids = []
  16.times do
    $managed_pids << fork do
      threads = []
      8.times do |i|
        threads << Thread.new do
          stub = Types::JobsService::Stub.new('localhost:3001', :this_channel_is_insecure)

          loop do
            job = Types::JobEnqueueRequest.new(
              priority: 1, payload: { shop_id: 1 }.to_json,
              concurrency: Types::JobConcurrencySetting.new(
                key: "omg",
                max_concurrency: 1,
                on_limit: Types::JobConcurrencySetting::OnLimit::DROP
              )
            )
            stub.enqueue_job(job)
            # sleep 0.1
          end
        end
      end

      16.times do |i|
        threads << Thread.new do
          stub = Types::JobsService::Stub.new('localhost:3001', :this_channel_is_insecure)

          loop do
            resp = stub.reserve_job(Google::Protobuf::Empty.new)
            case resp.result
            when :NONE
              # puts "[W#{i}] No job reserved. Waiting..."
              sleep 1
            when :RESERVED
              # puts resp.job.inspect
              unless resp.job.id
                raise "bad job id"
              end
              # puts "[W#{1}] Processing the job..."
              stub.ack_job(resp.job.id)
            end
          end
        end
      end
      threads.each(&:join)
    end
  end

  puts "Waiting for children..."

  Process.wait
end

main
