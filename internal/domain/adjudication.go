package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Adjudication 是用户裁决（D43）：对一条阻塞发现声明"接受当前版本"，绑定被裁决
// 裁定的证据基线，有效性按基线判定（D48）。记录只追加：撤回以新记录（Withdraws）
// 表达，历史不变、有效性可变。
type Adjudication struct {
	ID        string        `json:"id"`
	Finding   string        `json:"finding,omitempty"`
	Basis     EvidenceBasis `json:"basis,omitempty"`
	Reason    string        `json:"reason"`
	Withdraws string        `json:"withdraws,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
}

func (a Adjudication) Validate() error {
	if strings.TrimSpace(a.ID) == "" || strings.TrimSpace(a.Reason) == "" || a.CreatedAt.IsZero() {
		return fmt.Errorf("adjudication id, reason and creation time are required: %w", ErrInvalid)
	}
	switch {
	case a.Finding != "" && a.Withdraws != "":
		return fmt.Errorf("adjudication cannot both accept a finding and withdraw: %w", ErrInvalid)
	case a.Finding != "":
		if _, _, err := ParseFindingID(a.Finding); err != nil {
			return err
		}
		return validateEvidenceBasis("adjudication", a.Basis)
	case strings.TrimSpace(a.Withdraws) != "":
		if len(a.Basis.Documents)+len(a.Basis.Scopes)+len(a.Basis.Artifacts) != 0 {
			return fmt.Errorf("withdrawal record cannot carry a basis: %w", ErrInvalid)
		}
		return nil
	default:
		return fmt.Errorf("adjudication requires a finding or a withdrawal target: %w", ErrInvalid)
	}
}

// FindingID 是发现的稳定标识：<裁定 Operation id>/<发现序号>。
func FindingID(operationID string, index int) string {
	return operationID + "/" + strconv.Itoa(index)
}

func ParseFindingID(id string) (operationID string, index int, err error) {
	slash := strings.LastIndex(id, "/")
	if slash <= 0 || slash == len(id)-1 {
		return "", 0, fmt.Errorf("finding id %q must be <operation id>/<index>: %w", id, ErrInvalid)
	}
	index, err = strconv.Atoi(id[slash+1:])
	if err != nil || index < 0 {
		return "", 0, fmt.Errorf("finding id %q has an invalid index: %w", id, ErrInvalid)
	}
	return id[:slash], index, nil
}

// AcceptedFindings 汇总未被撤回的接受记录所指向的发现；基线是否仍成立由调用方先过滤。
func AcceptedFindings(adjudications []Adjudication) map[string]struct{} {
	withdrawn := make(map[string]struct{})
	for _, record := range adjudications {
		if record.Withdraws != "" {
			withdrawn[record.Withdraws] = struct{}{}
		}
	}
	accepted := make(map[string]struct{})
	for _, record := range adjudications {
		if _, ok := withdrawn[record.ID]; record.Finding == "" || ok {
			continue
		}
		accepted[record.Finding] = struct{}{}
	}
	return accepted
}
