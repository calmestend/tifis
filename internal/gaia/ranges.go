package gaia

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

// rangeReader reads a remote file in order while fetching the chunks ahead of
// the reader over several connections: the Gaia CDN gives about 1 MB/s per
// connection but scales with parallel range requests. At most window chunks
// are held in memory.
type rangeReader struct {
	ctx context.Context
	// One client per worker: the CDN always answers in HTTP/2, which would put
	// every request on a single connection, and the limit is per connection.
	clients []*http.Client
	url     string
	size    int64
	chunk   int64
	window  int

	results []chan chunkResult
	next    int // next chunk to start
	cur     int // chunk being read
	buf     []byte
	read    atomic.Int64 // bytes handed to the reader, for progress
}

type chunkResult struct {
	data []byte
	err  error
}

func newRangeReader(ctx context.Context, url string, size, chunk int64, window int) *rangeReader {
	n := int((size + chunk - 1) / chunk)
	r := &rangeReader{ctx: ctx, url: url, size: size, chunk: chunk, window: window,
		results: make([]chan chunkResult, n)}
	for range window {
		r.clients = append(r.clients, &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()})
	}
	for range window {
		r.start()
	}
	return r
}

func (r *rangeReader) start() {
	if r.next >= len(r.results) {
		return
	}
	i := r.next
	r.next++
	ch := make(chan chunkResult, 1)
	r.results[i] = ch
	go func() {
		lo := int64(i) * r.chunk
		hi := min(lo+r.chunk, r.size) - 1
		var res chunkResult
		for attempt := range 4 {
			if attempt > 0 {
				select {
				case <-time.After(time.Duration(attempt) * 2 * time.Second):
				case <-r.ctx.Done():
				}
			}
			res.data, res.err = r.fetch(r.clients[i%len(r.clients)], lo, hi)
			if res.err == nil || r.ctx.Err() != nil {
				break
			}
		}
		ch <- res
	}()
}

func (r *rangeReader) fetch(client *http.Client, lo, hi int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(r.ctx, http.MethodGet, r.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", lo, hi))
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusPartialContent {
		return nil, fmt.Errorf("gaia: range %d-%d: HTTP %d", lo, hi, res.StatusCode)
	}
	data, err := io.ReadAll(res.Body)
	if err == nil && int64(len(data)) != hi-lo+1 {
		err = fmt.Errorf("gaia: range %d-%d: got %d bytes", lo, hi, len(data))
	}
	return data, err
}

func (r *rangeReader) Read(p []byte) (int, error) {
	for len(r.buf) == 0 {
		if r.cur >= len(r.results) {
			return 0, io.EOF
		}
		var res chunkResult
		select {
		case res = <-r.results[r.cur]:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
		if res.err != nil {
			return 0, res.err
		}
		r.results[r.cur] = nil
		r.cur++
		r.buf = res.data
		r.start()
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	r.read.Add(int64(n))
	return n, nil
}
