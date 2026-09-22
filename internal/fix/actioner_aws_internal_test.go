package fix

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/codepipeline/types"
)

// The token is from the last poll, so someone may have decided in the console
// since. AWS reports that two ways; both mean "already decided".
func TestApprovalError_mapsStaleTokenErrors(t *testing.T) {
	for _, err := range []error{
		&types.InvalidApprovalTokenException{},
		&types.ApprovalAlreadyCompletedException{},
	} {
		if got := approvalError(err); !errors.Is(got, ErrApprovalAlreadyDecided) {
			t.Errorf("approvalError(%T) = %v, want ErrApprovalAlreadyDecided", err, got)
		}
	}
	other := errors.New("AccessDenied")
	if got := approvalError(other); !errors.Is(got, other) || errors.Is(got, ErrApprovalAlreadyDecided) {
		t.Errorf("approvalError(other) = %v, want it passed through", got)
	}
	if approvalError(nil) != nil {
		t.Error("approvalError(nil) should be nil")
	}
}

type codeErr string

func (c codeErr) Error() string     { return string(c) }
func (c codeErr) ErrorCode() string { return string(c) }

// A read-only profile can see the approval but not decide it. That is a
// configuration fact the user can act on, not a mystery AWS error.
func TestApprovalError_mapsAccessDenied(t *testing.T) {
	for _, code := range []string{"AccessDeniedException", "AccessDenied"} {
		if got := approvalError(codeErr(code)); !errors.Is(got, ErrApprovalNotPermitted) {
			t.Errorf("approvalError(%s) = %v, want ErrApprovalNotPermitted", code, got)
		}
	}
}
