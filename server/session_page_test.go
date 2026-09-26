package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPageOf(t *testing.T) {
	// 10 轮，每轮：用户一句 + 助手调 4 次工具（tool_use / tool_result 各一条）+ 助手回答
	var all []Entry
	for turn := 0; turn < 10; turn++ {
		all = append(all, Entry{Role: "user", Blocks: []Block{{T: "text", Text: "问题"}}})
		for i := 0; i < 4; i++ {
			id := string(rune('a'+turn)) + string(rune('0'+i))
			all = append(all,
				Entry{Role: "assistant", Blocks: []Block{{T: "tool_use", ID: id, Name: "Bash", Input: json.RawMessage(`{"command":"ls"}`)}}},
				Entry{Role: "user", Blocks: []Block{{T: "tool_result", ID: id, Text: "输出"}}})
		}
		all = append(all, Entry{Role: "assistant", Blocks: []Block{{T: "text", Text: "回答"}}})
	}
	// 每轮 10 条；最后 5 条落在一轮中间，应该往前挪到这一轮的开头
	p := pageOf(all, 0, 0, 5)
	if p.Start != 90 || len(p.Entries) != 10 || p.Total != 100 || !p.More || !isPrompt(p.Entries[0]) {
		t.Fatalf("最后一页：%+v", p)
	}
	older := pageOf(all, 0, p.Start, 5)
	if older.Start != 80 || len(older.Entries) != 10 || !older.More {
		t.Fatalf("再往前一页：start=%d len=%d", older.Start, len(older.Entries))
	}
	if first := pageOf(all, 0, 3, 5); first.Start != 0 || len(first.Entries) != 3 || first.More {
		t.Fatalf("到头：%+v", first)
	}
	// 只留了后 60 条（base=40）：下标照样按整个会话算
	kept := all[40:]
	if q := pageOf(kept, 40, 0, 5); q.Start != 90 || q.Total != 100 {
		t.Fatalf("base：%+v", q)
	}
	if q := pageOf(kept, 40, 50, 5); q.Start != 40 || len(q.Entries) != 10 || q.More {
		t.Fatalf("base 翻到头：start=%d len=%d more=%v", q.Start, len(q.Entries), q.More)
	}
	if q := pageOf(kept, 40, 30, 5); len(q.Entries) != 0 || q.More {
		t.Fatalf("比 base 还早的：%+v", q)
	}
	// 按行数截：每轮 3 行（提问、一串工具、回答），40 行大约 13 轮多，再对齐到那一轮开头
	var long []Entry
	for i := 0; i < 5; i++ {
		long = append(long, all...)
	}
	if q := pageOf(long, 0, 0, 1000); q.Start != 360 || len(q.Entries) != 140 {
		t.Fatalf("按行数截：start=%d len=%d", q.Start, len(q.Entries))
	}
	// 问答卡片不精简
	ask := []Entry{
		{Role: "assistant", Blocks: []Block{{T: "tool_use", ID: "q", Name: "AskUserQuestion", Input: json.RawMessage(`{"questions":[{"question":"` + strings.Repeat("长", 900) + `"}]}`)}}},
		{Role: "user", Blocks: []Block{{T: "tool_result", ID: "q", Text: "选了 A"}}},
	}
	if lite := liteEntries(ask); lite[0].Blocks[0].Lazy || lite[1].Blocks[0].Text != "选了 A" {
		t.Errorf("AskUserQuestion 不该精简：%+v", lite)
	}
	// 精简：工具参数小的原样留、结果正文拿掉；传进来的不能被改
	b := p.Entries[2].Blocks[0]
	if b.T != "tool_result" || b.Text != "" || !b.Lazy || all[92].Blocks[0].Text != "输出" {
		t.Errorf("结果应该拿掉正文、不动原数据：%+v", b)
	}
	// 这一页第一条是结果、它的 tool_use 在上一页：留着正文
	if lite := liteEntries(all[92:95]); lite[0].Blocks[0].Text != "输出" || lite[0].Blocks[0].Lazy {
		t.Errorf("没有对应 tool_use 的结果得留着：%+v", lite[0].Blocks[0])
	}
}

func TestSummaryInput(t *testing.T) {
	long := strings.Repeat("长", 5000)
	// 手写 JSON：字段顺序要保留（未知工具取第一个字符串字段当摘要）
	in := []byte(`{"command":"` + long + `","description":"跑测试","file_path":"/a/b.go","old_string":"` + long +
		`","changes":[{"path":"x.go","kind":"update","diff":"` + long + `"},{"path":"y.go","diff":"` + long + `"}]}`)
	out := summaryInput(in)
	if len(out) > 4000 {
		t.Fatalf("精简后还有 %d 字节", len(out))
	}
	var got struct {
		Command     string              `json:"command"`
		Description string              `json:"description"`
		FilePath    string              `json:"file_path"`
		Changes     []map[string]string `json:"changes"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err, string(out))
	}
	if !strings.HasPrefix(string(out), `{"command":`) || got.Description != "跑测试" || got.FilePath != "/a/b.go" ||
		len(got.Changes) != 2 || got.Changes[1]["path"] != "y.go" || !strings.HasSuffix(got.Command, "…") {
		t.Errorf("摘要字段丢了或顺序变了：%s", out)
	}
	if small := json.RawMessage(`{"pattern":"x"}`); string(summaryInput(small)) != string(small) {
		t.Error("小的原样留")
	}
}

func TestFindTools(t *testing.T) {
	es := []Entry{
		{Role: "assistant", Blocks: []Block{{T: "tool_use", ID: "t1", Input: json.RawMessage(`{"command":"ls"}`)}, {T: "tool_use", ID: "t2"}}},
		{Role: "user", Blocks: []Block{{T: "tool_result", ID: "t1", Text: "a.go", IsError: true}}},
	}
	got := findTools(es, map[string]bool{"t1": true, "t2": true, "nope": true})
	if string(got["t1"].Input) != `{"command":"ls"}` || got["t1"].Result.Text != "a.go" || !got["t1"].Result.IsError {
		t.Errorf("t1: %+v", got["t1"])
	}
	if got["t2"] == nil || got["t2"].Result != nil || got["nope"] != nil {
		t.Errorf("t2 还没结果、nope 不存在：%+v", got)
	}
}
