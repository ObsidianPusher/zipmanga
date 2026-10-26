package server

import (
	"fmt"
	"sync"

	"zipmanga/internal/comicfmt"
	"zipmanga/internal/db"
	"zipmanga/internal/mounts"
)

type comicPageLoadResult struct {
	path string
	err  error
}

type comicPageLoadTask struct {
	pageIdx  int
	comic    db.Comic
	provider mounts.Provider
	format   comicfmt.Format
	running  bool
	waiters  []chan comicPageLoadResult
}

type comicPageLoadQueue struct {
	server *Server
	key    string

	mu         sync.Mutex
	running    bool
	queue      []*comicPageLoadTask
	taskByPage map[int]*comicPageLoadTask
}

func comicPageLoadQueueKey(comic db.Comic, format comicfmt.Format) string {
	return fmt.Sprintf("page-load:%s:%d:%d:%d", format, comic.ID, comic.MtimeUnix, comic.Size)
}

func (s *Server) enqueueComicPageLoad(comic db.Comic, provider mounts.Provider, format comicfmt.Format, pageIdx int, wait bool) <-chan comicPageLoadResult {
	key := comicPageLoadQueueKey(comic, format)

	s.pageLoadMu.Lock()
	queue, ok := s.pageLoadQueues[key]
	if !ok {
		queue = &comicPageLoadQueue{
			server:     s,
			key:        key,
			taskByPage: make(map[int]*comicPageLoadTask, 32),
			queue:      make([]*comicPageLoadTask, 0, 32),
		}
		s.pageLoadQueues[key] = queue
	}
	s.pageLoadMu.Unlock()

	return queue.enqueue(comic, provider, format, pageIdx, wait)
}

func (s *Server) dropComicPageLoadQueue(key string, queue *comicPageLoadQueue) {
	s.pageLoadMu.Lock()
	defer s.pageLoadMu.Unlock()
	if current, ok := s.pageLoadQueues[key]; ok && current == queue {
		delete(s.pageLoadQueues, key)
	}
}

func (q *comicPageLoadQueue) enqueue(comic db.Comic, provider mounts.Provider, format comicfmt.Format, pageIdx int, wait bool) <-chan comicPageLoadResult {
	var waiter chan comicPageLoadResult
	if wait {
		waiter = make(chan comicPageLoadResult, 1)
	}

	q.mu.Lock()
	task, ok := q.taskByPage[pageIdx]
	if ok {
		if waiter != nil {
			task.waiters = append(task.waiters, waiter)
		}
		if !task.running && waiter != nil {
			q.promote(pageIdx)
		}
		if !q.running {
			q.running = true
			go q.run()
		}
		q.mu.Unlock()
		return waiter
	}

	task = &comicPageLoadTask{
		pageIdx:  pageIdx,
		comic:    comic,
		provider: provider,
		format:   format,
	}
	if waiter != nil {
		task.waiters = append(task.waiters, waiter)
	}
	q.taskByPage[pageIdx] = task
	if waiter != nil {
		q.queue = append([]*comicPageLoadTask{task}, q.queue...)
	} else {
		q.queue = append(q.queue, task)
	}
	if !q.running {
		q.running = true
		go q.run()
	}
	q.mu.Unlock()
	return waiter
}

func (q *comicPageLoadQueue) promote(pageIdx int) {
	if len(q.queue) <= 1 {
		return
	}
	for i := 1; i < len(q.queue); i++ {
		task := q.queue[i]
		if task == nil || task.pageIdx != pageIdx {
			continue
		}
		copy(q.queue[1:i+1], q.queue[0:i])
		q.queue[0] = task
		return
	}
}

func (q *comicPageLoadQueue) run() {
	q.server.pageLoadSem <- struct{}{}
	defer func() { <-q.server.pageLoadSem }()

	for {
		tasks := q.nextTasks(q.server.currentPageLoadParallelism())
		if len(tasks) == 0 {
			q.mu.Lock()
			if len(q.queue) == 0 {
				q.running = false
				q.mu.Unlock()
				q.server.dropComicPageLoadQueue(q.key, q)
				return
			}
			q.mu.Unlock()
			continue
		}
		q.runTasks(tasks)
	}
}

func (q *comicPageLoadQueue) nextTasks(limit int) []*comicPageLoadTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.queue) == 0 || limit <= 0 {
		return nil
	}
	if limit > len(q.queue) {
		limit = len(q.queue)
	}
	tasks := append([]*comicPageLoadTask(nil), q.queue[:limit]...)
	q.queue = q.queue[limit:]
	for _, task := range tasks {
		if task != nil {
			task.running = true
		}
	}
	return tasks
}

func (q *comicPageLoadQueue) runTasks(tasks []*comicPageLoadTask) {
	if len(tasks) == 0 {
		return
	}

	workerCount := q.server.currentPageLoadParallelism()
	if workerCount > len(tasks) {
		workerCount = len(tasks)
	}
	if workerCount < 1 {
		workerCount = 1
	}

	jobs := make(chan *comicPageLoadTask, len(tasks))
	var wg sync.WaitGroup

	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range jobs {
				if task == nil {
					continue
				}
				path, err := q.server.loadRawComicPageCacheDirect(task.comic, task.provider, task.format, task.pageIdx)
				// Return visible pages immediately; an unrelated slow prefetch
				// in the same batch must not hold their waiters.
				q.finishTask(task.pageIdx, comicPageLoadResult{path: path, err: err})
			}
		}()
	}

	for _, task := range tasks {
		jobs <- task
	}
	close(jobs)

	wg.Wait()
}

func (q *comicPageLoadQueue) finishTask(pageIdx int, result comicPageLoadResult) {
	q.mu.Lock()
	task := q.taskByPage[pageIdx]
	delete(q.taskByPage, pageIdx)
	var waiters []chan comicPageLoadResult
	if task != nil {
		waiters = append(waiters, task.waiters...)
	}
	q.mu.Unlock()

	for _, waiter := range waiters {
		if waiter == nil {
			continue
		}
		waiter <- result
		close(waiter)
	}
}
