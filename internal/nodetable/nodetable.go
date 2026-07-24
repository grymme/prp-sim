package nodetable

import "time"

type Entry struct {
	SrcMAC   string
	SeqNo    int
	LANID    int
	LastSeen time.Time
	ExpiryMs int
}

var table = make(map[string]Entry)

func key(srcMAC string, seq int) string {
	return srcMAC + "|" + string(rune(seq))
}

func Insert(srcMAC string, seq, lanID int) {
	table[key(srcMAC, seq)] = Entry{
		SrcMAC:   srcMAC,
		SeqNo:    seq,
		LANID:    lanID,
		LastSeen: time.Now(),
		ExpiryMs: 640,
	}
}

func Find(srcMAC string, seq int) bool {
	_, ok := table[key(srcMAC, seq)]
	return ok
}
