package repo

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// encodeCursor / decodeCursor 是列表游标：只编码最后一行的排序键，不透出
// SQL。不带 q 的列表 Rank 恒为 0，编码成 "created_at|id"；带 q 且已经翻到
// 包含匹配那一档时编码成 "rank|created_at|id"。解码两种都认。
//
// 游标只对发出它的那组查询参数有意义：换了 q 或时间窗口再用旧游标，结果
// 不保证连续。
func encodeCursor(k cursorKey) string {
	raw := k.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + strconv.FormatInt(k.ID, 10)
	if k.Rank != 0 {
		raw = strconv.Itoa(k.Rank) + "|" + raw
	}
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursorKey, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorKey{}, err
	}
	parts := strings.Split(string(raw), "|")
	var k cursorKey
	switch len(parts) {
	case 2:
	case 3:
		if k.Rank, err = strconv.Atoi(parts[0]); err != nil {
			return cursorKey{}, err
		}
		parts = parts[1:]
	default:
		return cursorKey{}, fmt.Errorf("格式不对：%q", string(raw))
	}
	if k.CreatedAt, err = time.Parse(time.RFC3339Nano, parts[0]); err != nil {
		return cursorKey{}, err
	}
	if k.ID, err = strconv.ParseInt(parts[1], 10, 64); err != nil {
		return cursorKey{}, err
	}
	return k, nil
}
