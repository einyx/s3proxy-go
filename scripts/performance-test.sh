#!/bin/bash

# Performance Test Script for S3 Proxy
set -e

echo "🚀 Running S3 Proxy Performance Tests"
echo "======================================"

# Build the project first
echo "📦 Building project..."
go build -o bin/s3proxy ./cmd/s3proxy

# Run comprehensive benchmarks
echo "🔥 Running benchmark tests..."
echo ""

# Test with different optimization levels
export GOMAXPROCS=8
export ENABLE_OBJECT_CACHE=true
export CACHE_MAX_MEMORY=1073741824  # 1GB
export CACHE_MAX_OBJECT_SIZE=10485760  # 10MB
export CACHE_TTL=5m

echo "⚡ Performance Benchmark Results:"
echo "================================="

# Run all benchmarks with optimizations
go test -bench=. -benchmem -benchtime=10s ./cmd/s3proxy/ | tee benchmark_results.txt

echo ""
echo "📊 Analyzing Results..."
echo "======================="

# Extract key metrics
echo "Key Performance Metrics:"
echo "------------------------"

# Get throughput for different object sizes
grep "BenchmarkS3ProxyGet" benchmark_results.txt | while read line; do
    size=$(echo $line | grep -o "size_[0-9]*B" | sed 's/size_//g' | sed 's/B//g')
    ops=$(echo $line | awk '{print $3}')
    ns_per_op=$(echo $line | awk '{print $4}')
    mb_per_sec=$(echo $line | awk '{print $6}')

    if [ ! -z "$size" ] && [ ! -z "$mb_per_sec" ]; then
        echo "📈 Object Size: ${size}B - Throughput: ${mb_per_sec} MB/s - ${ops} ops"
    fi
done

echo ""
echo "💾 Concurrent Performance:"
echo "-------------------------"

grep "BenchmarkConcurrentRequests" benchmark_results.txt | while read line; do
    size=$(echo $line | grep -o "concurrent_size_[0-9]*B" | sed 's/concurrent_size_//g' | sed 's/B//g')
    ops=$(echo $line | awk '{print $3}')
    ns_per_op=$(echo $line | awk '{print $4}')
    mb_per_sec=$(echo $line | awk '{print $6}')

    if [ ! -z "$size" ] && [ ! -z "$mb_per_sec" ]; then
        echo "🔀 Concurrent ${size}B: ${mb_per_sec} MB/s - ${ops} ops"
    fi
done

echo ""
echo "📏 Range Request Performance:"
echo "----------------------------"

grep "BenchmarkRangeRequests" benchmark_results.txt | while read line; do
    size=$(echo $line | grep -o "range_[0-9]*B" | sed 's/range_//g' | sed 's/B//g')
    ops=$(echo $line | awk '{print $3}')
    ns_per_op=$(echo $line | awk '{print $4}')
    mb_per_sec=$(echo $line | awk '{print $6}')

    if [ ! -z "$size" ] && [ ! -z "$mb_per_sec" ]; then
        echo "📐 Range ${size}B: ${mb_per_sec} MB/s - ${ops} ops"
    fi
done

echo ""
echo "📈 Memory Usage Analysis:"
echo "------------------------"

# Memory allocation metrics
grep "allocs/op" benchmark_results.txt | head -5 | while read line; do
    test_name=$(echo $line | awk '{print $1}')
    allocs=$(echo $line | awk '{print $5}')
    bytes_per_alloc=$(echo $line | awk '{print $7}')

    echo "🧠 ${test_name}: ${allocs} allocs/op, ${bytes_per_alloc} B/alloc"
done

echo ""
echo "🎯 Performance Summary:"
echo "======================"

# Calculate average throughput
total_throughput=0
count=0

grep "MB/s" benchmark_results.txt | while read line; do
    mb_per_sec=$(echo $line | grep -o "[0-9.]*[[:space:]]MB/s" | awk '{print $1}')
    if [ ! -z "$mb_per_sec" ]; then
        total_throughput=$(echo "$total_throughput + $mb_per_sec" | bc -l)
        count=$((count + 1))
    fi
done 2>/dev/null || true

echo "✅ All performance tests completed!"
echo "📁 Results saved to benchmark_results.txt"

# Check if we meet performance targets
echo ""
echo "🎯 Performance Targets Check:"
echo "=============================="

# Check for high throughput (>100 MB/s for large objects)
high_throughput_found=$(grep "size_104857600B" benchmark_results.txt | grep -o "[0-9.]*[[:space:]]MB/s" | awk '{print $1}' || echo "0")
if [ $(echo "$high_throughput_found > 100" | bc -l 2>/dev/null || echo "0") -eq 1 ]; then
    echo "✅ High throughput target met: ${high_throughput_found} MB/s for 100MB objects"
else
    echo "⚠️  High throughput target not met: ${high_throughput_found} MB/s (target: >100 MB/s)"
fi

# Check for low latency (< 1ms for small objects)
small_latency=$(grep "size_1024B" benchmark_results.txt | awk '{print $4}' | sed 's/ns\/op//g' || echo "999999999")
if [ $(echo "$small_latency < 1000000" | bc -l 2>/dev/null || echo "0") -eq 1 ]; then
    latency_ms=$(echo "scale=2; $small_latency / 1000000" | bc -l)
    echo "✅ Low latency target met: ${latency_ms}ms for 1KB objects"
else
    latency_ms=$(echo "scale=2; $small_latency / 1000000" | bc -l)
    echo "⚠️  Low latency target not met: ${latency_ms}ms (target: <1ms)"
fi

echo ""
echo "🔧 Optimization Recommendations:"
echo "================================"

# Analyze results and provide recommendations
avg_allocs=$(grep "allocs/op" benchmark_results.txt | awk '{sum += $5; count++} END {if(count > 0) print sum/count; else print 0}')
if [ $(echo "$avg_allocs > 10" | bc -l 2>/dev/null || echo "0") -eq 1 ]; then
    echo "💡 Consider reducing memory allocations (current avg: $avg_allocs allocs/op)"
fi

echo "💡 Enable object caching for frequently accessed files"
echo "💡 Use range requests for large file streaming"
echo "💡 Implement connection pooling for backend storage"
echo "💡 Monitor metrics at /metrics and /stats endpoints"

echo ""
echo "🏁 Performance testing complete!"
