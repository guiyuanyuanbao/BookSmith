package store

import (
	"path/filepath"
	"testing"

	"booksmith/internal/model"
)

// 用户禁用的书源在重新导入/仓库同步后不得被重新启用（enabled 保留）。
func TestUpsertSourcePreservesEnabled(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	bs := &model.BookSource{BookSourceURL: "https://a.com", BookSourceName: "甲源", Enabled: true}
	if err := st.UpsertSource(bs, false); err != nil {
		t.Fatal(err)
	}
	// 用户禁用
	if err := st.SetSourceEnabled("https://a.com", false); err != nil {
		t.Fatal(err)
	}
	// 重新导入（仓库同步场景：新 JSON 里 enabled=true）
	bs2 := &model.BookSource{BookSourceURL: "https://a.com", BookSourceName: "甲源（更新）", Enabled: true}
	if err := st.UpsertSource(bs2, false); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSource("https://a.com")
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("disabled source was re-enabled by upsert")
	}
	if got.BookSourceName != "甲源（更新）" {
		t.Errorf("name not updated: %q", got.BookSourceName)
	}
	// 校验结果同样保留：验证失败 → 重新导入 → verified 仍为 false
	st.SetSourceVerified("https://a.com", false, "超时", 1000)
	bs3 := &model.BookSource{BookSourceURL: "https://a.com", BookSourceName: "甲源", Enabled: true, Verified: true}
	if err := st.UpsertSource(bs3, false); err != nil {
		t.Fatal(err)
	}
	got2, _ := st.GetSource("https://a.com")
	if got2.Verified {
		t.Error("failed verification was overwritten by upsert")
	}
}
