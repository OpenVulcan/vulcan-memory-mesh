// profile_type_test.go verifies profile scope presence at the model-output boundary.
// profile_type_test.go 在模型输出边界验证画像范围字段的存在性。
package processor

import (
	"errors"
	"strings"
	"testing"

	logicdomain "github.com/openvulcan/vmm/internal/logic/domain"
)

// TestTurnAnalysisRequiresExplicitProfileType rejects omitted/null scopes while accepting the valid zero-valued user scope.
// TestTurnAnalysisRequiresExplicitProfileType 拒绝缺失或空值范围，同时接受合法的零值用户范围。
func TestTurnAnalysisRequiresExplicitProfileType(t *testing.T) {
	for _, field := range []string{"", `"profile_type":null,`, `"profile_type":-1,`, `"profile_type":4,`, `"profile_type":0,`} {
		t.Run(field, func(t *testing.T) {
			raw := `{"turn_id":1,"user_input_kind":"question","profile_nodes":[{` + field +
				`"content":"Use Chinese","evidence_source":"user_confirmed","admission":"keep","admission_reason":""}]}`
			analysis, err := parseTurnAnalysisResponse(raw)
			if field == `"profile_type":0,` {
				if err != nil || len(analysis.ProfileNodes) != 1 || analysis.ProfileNodes[0].ProfileType != logicdomain.ProfileTypeUser {
					t.Fatalf("explicit user scope: analysis=%+v err=%v", analysis, err)
				}
				return
			}
			var invalid logicdomain.InvalidLLMOutputError
			if !errors.As(err, &invalid) || !strings.Contains(invalid.Message, "profile_type") {
				t.Fatalf("expected profile_type validation error, got %v", err)
			}
		})
	}
}
