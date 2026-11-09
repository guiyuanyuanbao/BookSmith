package task

// syncBuffer 实现 io.Writer 的字节缓冲。
type syncBuffer struct{ data []byte }

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *syncBuffer) Bytes() []byte { return b.data }
func (b *syncBuffer) Len() int      { return len(b.data) }
