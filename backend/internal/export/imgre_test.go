package export

import "testing"

func TestContentImgRe(t *testing.T) {
	line := `&lt;img src=&quot;http://rs.sfacg.com/web/novel/images/uploadpic/2020/02/c9c066b3-2b8e-45c5-a72b-775ae94179ec.jpg&quot;&gt;`
	m := contentImgRe.FindStringSubmatch(line)
	t.Logf("match=%v url=%q", m != nil, func() string {
		if m != nil {
			if m[1] != "" {
				return m[1]
			}
			return m[2]
		}
		return ""
	}())
	if m == nil {
		t.Fatal("contentImgRe 未匹配转义 img 行")
	}
}
