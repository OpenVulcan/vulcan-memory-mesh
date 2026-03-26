package memorycleaner

import (
	"strings"
	"testing"
)

func TestCleanMemoryText_TableDriven(t *testing.T) {
	longChineseDoc := "```markdown\n" + strings.Join([]string{
		"第一部分：请根据以下业务规则输出结论，并保持正式、礼貌、可执行。",
		"第二部分：所有回答都必须先复述需求，再给出风险点和执行建议。",
		"第三部分：如果出现 if a > 0 这样的伪代码，也只是解释业务流程，不代表真的要执行程序。",
		"第四部分：这里会提到 for 循环、[]string{\"a\", \"b\"}、HashMap.put 之类术语，但本质仍然是中文文档。",
		"第五部分：客服场景中，需要优先识别用户诉求，再结合上下文判断是否需要升级人工。",
		"第六部分：对账场景中，允许输出步骤清单，但不允许遗漏边界条件与失败回滚方案。",
		"第七部分：这是一段很长的 Markdown 业务文档，用来证明中文密度足够高时，不应该被误判成机器代码。",
		"第八部分：你需要把用户体验、业务约束、接口幂等性和异常提示全部说清楚。",
		"第九部分：若需要举例，请使用接近日常中文的句子，不要只给出程序片段。",
		"第十部分：最终答案要让产品、研发、测试和客服都能读懂。",
		"第十一部分：这段文本超过十行且明显超过三百字符，但仍然必须被完整保留。",
	}, "\n") + "\n```"

	longEnglishPrompt := "```text\n" + strings.Join([]string{
		"You are an assistant helping a support engineer summarize incidents in clear business language.",
		"First restate the user's goal in one sentence before giving any technical explanation.",
		"Then describe the impact, the likely cause, and the safest next action in simple English.",
		"Do not assume the reader understands code, logs, stack traces, or deployment internals.",
		"If a sample mentions if x > 0 or return err, treat it as an explanatory example only.",
		"Prefer complete sentences with context instead of dense shorthand or shell snippets.",
		"Highlight risks, tradeoffs, and escalation conditions in plain language.",
		"Keep answers concise but never omit the human intent behind the problem.",
		"This fenced block is intentionally long so the test can prove prose is preserved.",
		"It contains many words, few symbols, and should not be collapsed as machine text.",
	}, "\n") + "\n```"

	shortFence := "用户给了一个很短的配置：\n```yaml\nname: demo\nport: 8080\n```\n这段要保留。"

	longStackInFence := "```java\n" + strings.Join([]string{
		"java.lang.NullPointerException: Cannot invoke \"String.length()\" because \"name\" is null",
		"    at com.example.MyClass.process(MyClass.java:42)",
		"    at com.example.Service.run(Service.java:88)",
		"    at com.example.Handler.handle(Handler.java:103)",
		"    at com.example.Controller.execute(Controller.java:144)",
		"    at com.example.Router.dispatch(Router.java:55)",
		"    at com.example.FilterChain.next(FilterChain.java:29)",
		"    at com.example.Server.handle(Server.java:240)",
		"    at com.example.Server.handle(Server.java:241)",
		"    at com.example.Server.handle(Server.java:242)",
		"    at com.example.Server.handle(Server.java:243)",
		"    at com.example.Server.handle(Server.java:244)",
		"    at com.example.Server.handle(Server.java:245)",
		"    at com.example.Server.handle(Server.java:246)",
		"    at com.example.Server.handle(Server.java:247)",
		"    at java.base/java.lang.Thread.run(Thread.java:833)",
		"exit status 1",
	}, "\n") + "\n```"

	bareGoStack := "我运行了这段 Go 代码报错了：\n\n" + strings.Join([]string{
		"goroutine 1 [running]:",
		"main.main()",
		"\t/app/main.go:15 +0x2b",
		"runtime.main()",
		"\t/usr/local/go/src/runtime/proc.go:272 +0x28b",
		"runtime.goexit()",
		"\t/usr/local/go/src/runtime/asm_amd64.s:1700 +0x1",
		"goroutine 2 [GC sweep wait]:",
		"runtime.gopark(0x0?, 0x0?, 0x0?, 0x0?, 0x0?)",
		"\t/usr/local/go/src/runtime/proc.go:424 +0xce",
		"runtime.goparkunlock(...)",
		"\t/usr/local/go/src/runtime/proc.go:430",
		"runtime.bgsweep(0xc000028080)",
		"\t/usr/local/go/src/runtime/mgcsweep.go:317 +0xdf",
		"runtime.goexit()",
		"\t/usr/local/go/src/runtime/asm_amd64.s:1700 +0x1",
		"exit status 2",
	}, "\n") + "\n\n请问这是怎么回事？"

	bareJSON := "接口返回体太长了，核心部分如下：\n\n" + strings.Join([]string{
		"[",
		"  {\"id\":1,\"name\":\"alpha\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-001\",\"retry\":false}},",
		"  {\"id\":2,\"name\":\"beta\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-002\",\"retry\":false}},",
		"  {\"id\":3,\"name\":\"gamma\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-003\",\"retry\":false}},",
		"  {\"id\":4,\"name\":\"delta\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-004\",\"retry\":false}},",
		"  {\"id\":5,\"name\":\"epsilon\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-005\",\"retry\":false}},",
		"  {\"id\":6,\"name\":\"zeta\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-006\",\"retry\":false}},",
		"  {\"id\":7,\"name\":\"eta\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-007\",\"retry\":false}},",
		"  {\"id\":8,\"name\":\"theta\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-008\",\"retry\":false}},",
		"  {\"id\":9,\"name\":\"iota\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-009\",\"retry\":false}},",
		"  {\"id\":10,\"name\":\"kappa\",\"status\":\"ok\",\"meta\":{\"trace\":\"t-010\",\"retry\":false}}",
		"]",
	}, "\n") + "\n\n你帮我看看主要字段就行。"

	mixedChineseParagraph := strings.Repeat("这个函数的逻辑是：首先 if a > 0，然后我们在 for 循环里拼接 []string{\"a\", \"b\"}，最后把结果返回给调用方，并结合业务规则判断是否要重试。", 4)

	multiRunText := "先描述业务背景。\n\n" + strings.Join([]string{
		"2026-03-26 10:00:00 INFO boot start worker=1",
		"2026-03-26 10:00:01 INFO connect db=primary retry=0",
		"2026-03-26 10:00:02 WARN cache miss key=session:1",
		"2026-03-26 10:00:03 WARN cache miss key=session:2",
		"2026-03-26 10:00:04 ERROR downstream timeout trace=t-1001",
		"2026-03-26 10:00:05 ERROR downstream timeout trace=t-1002",
		"2026-03-26 10:00:06 ERROR downstream timeout trace=t-1003",
		"2026-03-26 10:00:07 ERROR downstream timeout trace=t-1004",
		"2026-03-26 10:00:08 ERROR downstream timeout trace=t-1005",
		"2026-03-26 10:00:09 ERROR downstream timeout trace=t-1006",
		"2026-03-26 10:00:10 ERROR downstream timeout trace=t-1007",
		"2026-03-26 10:00:11 ERROR downstream timeout trace=t-1008",
		"2026-03-26 10:00:12 ERROR downstream timeout trace=t-1009",
	}, "\n") + "\n\n这里是人工总结：用户主要关心的是为什么支付回调变慢，以及是否需要手动补偿。\n\n" + strings.Join([]string{
		"[",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1001\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1002\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1003\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1004\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1005\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1006\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1007\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1008\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1009\",\"retry\":0},",
		"  {\"event\":\"pay_timeout\",\"trace\":\"t-1010\",\"retry\":0}",
		"]",
	}, "\n") + "\n\n最后请给我一个简短结论。"

	tests := []struct {
		name         string
		input        string
		wantContains []string
		wantAbsent   []string
		wantSame     bool
	}{
		{
			name:     "short text immunity",
			input:    "HashMap 的 put 方法是做什么的？",
			wantSame: true,
		},
		{
			name:     "inline code in chinese paragraph preserved",
			input:    mixedChineseParagraph,
			wantSame: true,
		},
		{
			name:         "short fenced block preserved",
			input:        shortFence,
			wantContains: []string{"```yaml", "name: demo", "port: 8080", "这段要保留"},
			wantAbsent:   []string{"已按策略折叠"},
		},
		{
			name:         "long fenced pure stack trace compressed with head tail preserved",
			input:        longStackInFence,
			wantContains: []string{"```java", "java.lang.NullPointerException", "at com.example.MyClass.process", "[中间", "Thread.run(Thread.java:833)", "exit status 1", "```"},
			wantAbsent:   []string{"Server.handle(Server.java:244)", "Server.handle(Server.java:245)"},
		},
		{
			name:         "long chinese markdown doc in fence preserved",
			input:        longChineseDoc,
			wantContains: []string{"```markdown", "第一部分", "第十一部分", "HashMap.put", "```"},
			wantAbsent:   []string{"已按策略折叠"},
		},
		{
			name:         "long english prose in fence preserved",
			input:        longEnglishPrompt,
			wantContains: []string{"You are an assistant helping a support engineer", "simple English", "```text"},
			wantAbsent:   []string{"已按策略折叠"},
		},
		{
			name:         "bare go stack compressed and natural question preserved",
			input:        bareGoStack,
			wantContains: []string{"我运行了这段 Go 代码报错了", "goroutine 1 [running]:", "main.main()", "[中间", "exit status 2", "请问这是怎么回事"},
			wantAbsent:   []string{"runtime.goparkunlock(...)", "runtime.bgsweep(0xc000028080)"},
		},
		{
			name:         "bare json compressed and surrounding language preserved",
			input:        bareJSON,
			wantContains: []string{"接口返回体太长了", "[中间", "你帮我看看主要字段就行"},
			wantAbsent:   []string{"\"trace\":\"t-008\"", "\"trace\":\"t-009\""},
		},
		{
			name:         "multiple machine runs compressed independently",
			input:        multiRunText,
			wantContains: []string{"先描述业务背景", "[中间", "人工总结", "最后请给我一个简短结论"},
			wantAbsent:   []string{"trace=t-1006", "\"trace\":\"t-1008\""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CleanMemoryText(tt.input)
			if tt.wantSame && got != tt.input {
				t.Fatalf("expected same output\ninput:\n%s\n\ngot:\n%s", tt.input, got)
			}
			for _, sub := range tt.wantContains {
				if !strings.Contains(got, sub) {
					t.Fatalf("expected output to contain %q\nfull output:\n%s", sub, got)
				}
			}
			for _, sub := range tt.wantAbsent {
				if strings.Contains(got, sub) {
					t.Fatalf("expected output to NOT contain %q\nfull output:\n%s", sub, got)
				}
			}
		})
	}
}

func TestCleanMemoryText_HeadTailPreservationInFence(t *testing.T) {
	input := "```java\n" + strings.Join([]string{
		"java.lang.IllegalStateException: broken state",
		"    at a.A.one(A.java:1)",
		"    at a.A.two(A.java:2)",
		"    at a.A.three(A.java:3)",
		"    at a.A.four(A.java:4)",
		"    at a.A.five(A.java:5)",
		"    at a.A.six(A.java:6)",
		"    at a.A.seven(A.java:7)",
		"    at a.A.eight(A.java:8)",
		"    at a.A.nine(A.java:9)",
		"    at a.A.ten(A.java:10)",
		"    at java.base.Thread.run(Thread.java:833)",
		"exit status 1",
	}, "\n") + "\n```"

	got := CleanMemoryText(input)

	mustContainInOrder(t, got,
		"java.lang.IllegalStateException: broken state",
		"at a.A.one(A.java:1)",
		"at a.A.two(A.java:2)",
		"[中间",
		"at java.base.Thread.run(Thread.java:833)",
		"exit status 1",
	)
	if strings.Contains(got, "at a.A.five(A.java:5)") {
		t.Fatalf("middle lines should be removed\n%s", got)
	}
}

func TestCleanMemoryText_LongChineseDocOutsideFenceNotKilled(t *testing.T) {
	input := strings.Join([]string{
		"下面是一段很长的中文业务说明，用来证明即使文本中夹杂 if a > 0、for、[]string{\"a\", \"b\"} 这样的符号，也不应该被清洗。",
		"我们真正要保留的是人类的意图：用户想知道订单为什么会失败、失败后该怎么补偿、以及是否需要给客服提示。",
		"因此历史记忆里最有价值的是业务上下文、责任边界、失败条件和下一步动作，而不是机械地删除整段中文解释。",
		"如果你误把这类文本当成代码，那么后续检索时模型会失去真正重要的信息，这正是本模块必须避免的事情。",
		"为了提高误杀防御，这里继续补充很多中文内容，让整段文本超过阈值，并且明显具有自然语言特征。",
		"当文本包含大量中文叙述、完整句子和业务语义时，就算有一些括号、数组字面量或方法名，也必须保留原文。",
	}, "\n")

	got := CleanMemoryText(input)
	if got != input {
		t.Fatalf("long chinese prose should be preserved\ninput:\n%s\n\ngot:\n%s", input, got)
	}
}

func TestCleanMemoryText_RootCauseAnchorPreserved(t *testing.T) {
	input := "```java\n" + strings.Join([]string{
		"java.lang.RuntimeException: top level failed",
		"    at app.Service.run(Service.java:10)",
		"    at app.Controller.handle(Controller.java:20)",
		"    at app.Controller.handle(Controller.java:21)",
		"    at app.Controller.handle(Controller.java:22)",
		"    at app.Controller.handle(Controller.java:23)",
		"    at app.Controller.handle(Controller.java:24)",
		"    at app.Controller.handle(Controller.java:25)",
		"Caused by: java.sql.SQLException: connection reset by peer",
		"    at db.Driver.query(Driver.java:201)",
		"    at db.Driver.query(Driver.java:202)",
		"    at db.Driver.query(Driver.java:203)",
		"    at db.Driver.query(Driver.java:204)",
		"    at db.Driver.query(Driver.java:205)",
		"    at java.base/java.lang.Thread.run(Thread.java:833)",
		"exit status 1",
	}, "\n") + "\n```"

	got := CleanMemoryText(input)
	if !strings.Contains(got, "Caused by: java.sql.SQLException: connection reset by peer") {
		t.Fatalf("root cause anchor should be preserved\n%s", got)
	}
	if count := strings.Count(got, "[中间"); count < 2 {
		t.Fatalf("expected fold marker around anchor, got %d\n%s", count, got)
	}
	if strings.Contains(got, "at db.Driver.query(Driver.java:202)") {
		t.Fatalf("middle non-anchor lines should still be folded\n%s", got)
	}
}

func TestCleanMemoryText_ChineseJSONInFenceStillCompressed(t *testing.T) {
	input := "```json\n" + strings.Join([]string{
		"[",
		"  {\"商品\":\"红富士苹果礼盒\",\"描述\":\"精选山东产区，果径均匀，适合送礼\",\"价格\":128},",
		"  {\"商品\":\"有机香蕉组合\",\"描述\":\"软糯香甜，适合家庭早餐与加餐\",\"价格\":56},",
		"  {\"商品\":\"蓝莓大果盒\",\"描述\":\"冷链配送，到手可直接食用\",\"价格\":66},",
		"  {\"商品\":\"车厘子双J\",\"描述\":\"节庆热卖，口感脆甜，含中文详细文案\",\"价格\":188},",
		"  {\"商品\":\"芒果礼袋\",\"描述\":\"果香浓郁，适合制作甜品和果切\",\"价格\":79},",
		"  {\"商品\":\"猕猴桃整箱\",\"描述\":\"维C丰富，酸甜平衡，支持生鲜次日达\",\"价格\":92},",
		"  {\"商品\":\"草莓优选装\",\"描述\":\"颗粒饱满，适合直接食用或做蛋糕装饰\",\"价格\":88},",
		"  {\"商品\":\"橙子家庭装\",\"描述\":\"汁水充沛，适合榨汁，中文字段很多很多\",\"价格\":68},",
		"  {\"商品\":\"葡萄尝鲜装\",\"描述\":\"果粉完整，口感清甜，运输稳定\",\"价格\":73},",
		"  {\"商品\":\"梨子大箱\",\"描述\":\"清甜多汁，适合家庭囤货与员工福利\",\"价格\":82}",
		"]",
	}, "\n") + "\n```"

	got := CleanMemoryText(input)
	if !strings.Contains(got, "[中间") {
		t.Fatalf("Chinese JSON should still be compressed\n%s", got)
	}
	mustContainInOrder(t, got, "```json", "[", "红富士苹果礼盒", "[中间", "梨子大箱", "]", "```")
	if strings.Contains(got, "草莓优选装") || strings.Contains(got, "橙子家庭装") {
		t.Fatalf("middle Chinese JSON rows should be folded\n%s", got)
	}
}

func TestCleanMemoryText_SingleLongBase64LineTruncated(t *testing.T) {
	payload := "BASE64BEGIN" + strings.Repeat("QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo", 160) + "BASE64END"
	input := "下面是一行超长 token：\n" + payload + "\n请只保留关键上下文。"

	got := CleanMemoryText(input)
	if !strings.Contains(got, "单行超长机器文本已截断") {
		t.Fatalf("expected horizontal truncation\n%s", got)
	}
	if !strings.Contains(got, "BASE64BEGIN") || !strings.Contains(got, "BASE64END") {
		t.Fatalf("head and tail of long line should be preserved\n%s", got)
	}
	if strings.Contains(got, payload) {
		t.Fatalf("full payload should not remain intact\n%s", got)
	}
	if !strings.Contains(got, "下面是一行超长 token") || !strings.Contains(got, "请只保留关键上下文") {
		t.Fatalf("surrounding natural language should be preserved\n%s", got)
	}
}

func TestCleanMemoryText_EnglishRegexDiscussionPreserved(t *testing.T) {
	input := strings.Join([]string{
		"In plain English, the regex ^[a-z0-9._%+-]+@[a-z0-9.-]+\\.[a-z]{2,}$ is just validating the rough shape of an email address, and grep -E lets you use a similar idea from the shell.",
		"I am not pasting a log, stack trace, or minified program here; I am explaining what anchors, quantifiers, and character classes mean to another engineer in ordinary prose.",
		"When a message discusses sed, awk, or a tiny command example inside a paragraph, the cleaner should preserve that English technical context exactly instead of collapsing it as machine text.",
	}, "\n")

	got := CleanMemoryText(input)
	if got != input {
		t.Fatalf("English technical discussion should be preserved\ninput:\n%s\n\ngot:\n%s", input, got)
	}
}

func mustContainInOrder(t *testing.T, s string, subs ...string) {
	t.Helper()
	pos := 0
	for _, sub := range subs {
		idx := strings.Index(s[pos:], sub)
		if idx < 0 {
			t.Fatalf("expected %q after position %d\nfull output:\n%s", sub, pos, s)
		}
		pos += idx + len(sub)
	}
}
