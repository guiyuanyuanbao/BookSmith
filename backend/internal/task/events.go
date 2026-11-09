// Package task 实现下载任务管理：任务队列、工作池、进度事件、断点续传、多格式导出。
package task

import (
	"fmt"
	"sync"
	"time"
)

// Event SSE 事件。
type Event struct {
	Type   string `json:"type"` // task.progress / task.done / task.error / search.progress
	TaskID int64  `json:"taskId,omitempty"`
	Data   any    `json:"data,omitempty"`
	Time   int64  `json:"time"`
}

// EventBus 全局事件总线（SSE 用）。
type EventBus struct {
	mu   sync.Mutex
	subs map[int64]chan Event
	next int64
}

// NewEventBus 创建事件总线。
func NewEventBus() *EventBus {
	return &EventBus{subs: map[int64]chan Event{}}
}

// Subscribe 订阅。
func (b *EventBus) Subscribe() (int64, chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	id := b.next
	ch := make(chan Event, 64)
	b.subs[id] = ch
	return id, ch
}

// Unsubscribe 退订。
func (b *EventBus) Unsubscribe(id int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ch, ok := b.subs[id]; ok {
		delete(b.subs, id)
		close(ch)
	}
}

// Publish 广播事件（非阻塞，慢消费者丢弃）。
func (b *EventBus) Publish(ev Event) {
	ev.Time = time.Now().UnixMilli()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// ProgressData 任务进度载荷。
type ProgressData struct {
	BookName   string `json:"bookName"`
	Done       int    `json:"done"`
	Failed     int    `json:"failed"`
	Total      int    `json:"total"`
	Status     string `json:"status"`
	Phase      string `json:"phase"` // toc / content / export
	ExportPath string `json:"exportPath,omitempty"`
	Msg        string `json:"msg,omitempty"`
}

// String 事件描述。
func (e Event) String() string {
	return fmt.Sprintf("[%d] %s", e.TaskID, e.Type)
}
