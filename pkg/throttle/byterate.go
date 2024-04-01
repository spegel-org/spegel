package throttle

import (
	"fmt"
	"regexp"
	"strconv"
)

var unmarshalRegex = regexp.MustCompile(`^(\d+)\s?([KMGT]?Bps)$`)

type Byterate int64

const (
	Bps  Byterate = 1
	KBps          = 1024 * Bps
	MBps          = 1024 * KBps
	GBps          = 1024 * MBps
	TBps          = 1024 * GBps
)

func (br *Byterate) UnmarshalText(b []byte) error {
	comps := unmarshalRegex.FindStringSubmatch(string(b))
	if len(comps) != 3 {
		return fmt.Errorf("invalid byterate format %s should be n Bps, n KBps, n MBps, n GBps, or n TBps", string(b))
	}
	v, err := strconv.Atoi(comps[1])
	if err != nil {
		return err
	}
	unitStr := comps[2]
	var unit Byterate
	switch unitStr {
	case "Bps":
		unit = Bps
	case "KBps":
		unit = KBps
	case "MBps":
		unit = MBps
	case "GBps":
		unit = GBps
	case "TBps":
		unit = TBps
	default:
		return fmt.Errorf("unknown unit %s", unitStr)
	}
	*br = Byterate(v) * unit
	return nil
}
