package mysql

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/gongzhxu/go-mysql/utils"
	"github.com/google/uuid"
	"github.com/pingcap/errors"
)

// Like MySQL GTID Interval struct, [start, stop), left closed and right open
// See MySQL rpl_gtid.h
type Interval struct {
	// The first GID of this interval.
	Start int64
	// The first GID after this interval.
	Stop int64
}

// Interval is [start, stop), but the GTID string's format is [n] or [n1-n2], closed interval
func parseInterval(str string) (i Interval, err error) {
	p := strings.Split(str, "-")
	switch len(p) {
	case 1:
		i.Start, err = strconv.ParseInt(p[0], 10, 64)
		i.Stop = i.Start + 1
	case 2:
		i.Start, err = strconv.ParseInt(p[0], 10, 64)
		if err == nil {
			i.Stop, err = strconv.ParseInt(p[1], 10, 64)
			i.Stop++
		}
	default:
		err = errors.Errorf("invalid interval format, must n[-n]")
	}

	if err != nil {
		return
	}

	if i.Stop <= i.Start {
		err = errors.Errorf("invalid interval format, must n[-n] and the end must >= start")
	}

	return
}

func (i Interval) String() string {
	if i.Stop == i.Start+1 {
		return fmt.Sprintf("%d", i.Start)
	} else {
		return fmt.Sprintf("%d-%d", i.Start, i.Stop-1)
	}
}

type IntervalSlice []Interval

func (s IntervalSlice) Len() int {
	return len(s)
}

func (s IntervalSlice) Less(i, j int) bool {
	if s[i].Start < s[j].Start {
		return true
	} else if s[i].Start > s[j].Start {
		return false
	} else {
		return s[i].Stop < s[j].Stop
	}
}

func (s IntervalSlice) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}

func (s IntervalSlice) Sort() {
	sort.Sort(s)
}

func (s IntervalSlice) Normalize() IntervalSlice {
	var n IntervalSlice
	if len(s) == 0 {
		return n
	}

	s.Sort()

	n = append(n, s[0])

	for i := 1; i < len(s); i++ {
		last := n[len(n)-1]
		if s[i].Start > last.Stop {
			n = append(n, s[i])
			continue
		} else {
			stop := s[i].Stop
			if last.Stop > stop {
				stop = last.Stop
			}
			n[len(n)-1] = Interval{last.Start, stop}
		}
	}

	return n
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func (s *IntervalSlice) InsertInterval(interval Interval) {
	var (
		count int
		i     int
	)

	*s = append(*s, interval)
	total := len(*s)
	for i = total - 1; i > 0; i-- {
		if (*s)[i].Stop < (*s)[i-1].Start {
			(*s)[i], (*s)[i-1] = (*s)[i-1], (*s)[i]
		} else if (*s)[i].Start > (*s)[i-1].Stop {
			break
		} else {
			(*s)[i-1].Start = min((*s)[i-1].Start, (*s)[i].Start)
			(*s)[i-1].Stop = max((*s)[i-1].Stop, (*s)[i].Stop)
			count++
		}
	}
	if count > 0 {
		i++
		if i+count < total {
			copy((*s)[i:], (*s)[i+count:])
		}
		*s = (*s)[:total-count]
	}
}

// Contain returns true if sub in s
func (s IntervalSlice) Contain(sub IntervalSlice) bool {
	j := 0
	for i := 0; i < len(sub); i++ {
		for ; j < len(s); j++ {
			if sub[i].Start > s[j].Stop {
				continue
			} else {
				break
			}
		}
		if j == len(s) {
			return false
		}

		if sub[i].Start < s[j].Start || sub[i].Stop > s[j].Stop {
			return false
		}
	}

	return true
}

func (s IntervalSlice) Equal(o IntervalSlice) bool {
	if len(s) != len(o) {
		return false
	}

	for i := 0; i < len(s); i++ {
		if s[i].Start != o[i].Start || s[i].Stop != o[i].Stop {
			return false
		}
	}

	return true
}

func (s IntervalSlice) Compare(o IntervalSlice) int {
	if s.Equal(o) {
		return 0
	} else if s.Contain(o) {
		return 1
	} else {
		return -1
	}
}

// Refer http://dev.mysql.com/doc/refman/5.6/en/replication-gtids-concepts.html
type UUIDSet struct {
	SID uuid.UUID

	Intervals IntervalSlice
}

func ParseUUIDSet(str string) (*UUIDSet, error) {
	str = strings.TrimSpace(str)
	sep := strings.Split(str, ":")
	if len(sep) < 2 {
		return nil, errors.Errorf("invalid GTID format, must UUID:interval[:interval]")
	}

	var err error
	s := new(UUIDSet)
	if s.SID, err = uuid.Parse(sep[0]); err != nil {
		return nil, errors.Trace(err)
	}

	// Handle interval
	for i := 1; i < len(sep); i++ {
		if in, err := parseInterval(sep[i]); err != nil {
			return nil, errors.Trace(err)
		} else {
			s.Intervals = append(s.Intervals, in)
		}
	}

	s.Intervals = s.Intervals.Normalize()

	return s, nil
}

func NewUUIDSet(sid uuid.UUID, in ...Interval) *UUIDSet {
	s := new(UUIDSet)
	s.SID = sid

	s.Intervals = in
	s.Intervals = s.Intervals.Normalize()

	return s
}

func (s *UUIDSet) Contain(sub *UUIDSet) bool {
	if s.SID != sub.SID {
		return false
	}

	return s.Intervals.Contain(sub.Intervals)
}

func (s *UUIDSet) Bytes() []byte {
	var buf bytes.Buffer

	buf.WriteString(s.SID.String())

	for _, i := range s.Intervals {
		buf.WriteString(":")
		buf.WriteString(i.String())
	}

	return buf.Bytes()
}

func (s *UUIDSet) AddInterval(in IntervalSlice) {
	s.Intervals = append(s.Intervals, in...)
	s.Intervals = s.Intervals.Normalize()
}

func (s *UUIDSet) MinusInterval(in IntervalSlice) {
	var n IntervalSlice
	in = in.Normalize()

	i, j := 0, 0
	var minuend Interval
	var subtrahend Interval
	for i < len(s.Intervals) {
		if minuend.Stop != s.Intervals[i].Stop { // `i` changed?
			minuend = s.Intervals[i]
		}
		if j < len(in) {
			subtrahend = in[j]
		} else {
			subtrahend = Interval{math.MaxInt64, math.MaxInt64}
		}

		if minuend.Stop <= subtrahend.Start {
			// no overlapping
			n = append(n, minuend)
			i++
		} else if minuend.Start >= subtrahend.Stop {
			// no overlapping
			j++
		} else {
			if minuend.Start < subtrahend.Start && minuend.Stop <= subtrahend.Stop {
				n = append(n, Interval{minuend.Start, subtrahend.Start})
				i++
			} else if minuend.Start >= subtrahend.Start && minuend.Stop > subtrahend.Stop {
				minuend = Interval{subtrahend.Stop, minuend.Stop}
				j++
			} else if minuend.Start >= subtrahend.Start && minuend.Stop <= subtrahend.Stop {
				// minuend is completely removed
				i++
			} else if minuend.Start < subtrahend.Start && minuend.Stop > subtrahend.Stop {
				n = append(n, Interval{minuend.Start, subtrahend.Start})
				minuend = Interval{subtrahend.Stop, minuend.Stop}
				j++
			} else {
				panic("should never be here")
			}
		}
	}

	s.Intervals = n.Normalize()
}

func (s *UUIDSet) String() string {
	return utils.ByteSliceToString(s.Bytes())
}

func (s *UUIDSet) encode(w io.Writer) {
	b, _ := s.SID.MarshalBinary()

	_, _ = w.Write(b)
	n := int64(len(s.Intervals))

	_ = binary.Write(w, binary.LittleEndian, n)

	for _, i := range s.Intervals {
		_ = binary.Write(w, binary.LittleEndian, i.Start)
		_ = binary.Write(w, binary.LittleEndian, i.Stop)
	}
}

func (s *UUIDSet) Encode() []byte {
	var buf bytes.Buffer

	s.encode(&buf)

	return buf.Bytes()
}

func (s *UUIDSet) decode(data []byte) (int, error) {
	if len(data) < 24 {
		return 0, errors.Errorf("invalid uuid set buffer, less 24")
	}

	pos := 0
	var err error
	if s.SID, err = uuid.FromBytes(data[0:16]); err != nil {
		return 0, err
	}
	pos += 16

	n := int64(binary.LittleEndian.Uint64(data[pos : pos+8]))
	pos += 8
	if len(data) < int(16*n)+pos {
		return 0, errors.Errorf("invalid uuid set buffer, must %d, but %d", pos+int(16*n), len(data))
	}

	s.Intervals = make([]Interval, 0, n)

	var in Interval
	for i := int64(0); i < n; i++ {
		in.Start = int64(binary.LittleEndian.Uint64(data[pos : pos+8]))
		pos += 8
		in.Stop = int64(binary.LittleEndian.Uint64(data[pos : pos+8]))
		pos += 8
		s.Intervals = append(s.Intervals, in)
	}

	return pos, nil
}

func (s *UUIDSet) Decode(data []byte) error {
	n, err := s.decode(data)
	if n != len(data) {
		return errors.Errorf("invalid uuid set buffer, must %d, but %d", n, len(data))
	}
	return err
}

func (s *UUIDSet) Clone() *UUIDSet {
	clone := new(UUIDSet)
	clone.SID = s.SID
	clone.Intervals = make([]Interval, len(s.Intervals))
	copy(clone.Intervals, s.Intervals)
	return clone
}

type MysqlGTIDSet struct {
	Sets map[string]*UUIDSet
}

var _ GTIDSet = &MysqlGTIDSet{}

func ParseMysqlGTIDSet(str string) (GTIDSet, error) {
	s := new(MysqlGTIDSet)
	s.Sets = make(map[string]*UUIDSet)
	if str == "" {
		return s, nil
	}

	sp := strings.Split(str, ",")

	// todo, handle redundant same uuid
	for i := 0; i < len(sp); i++ {
		if set, err := ParseUUIDSet(sp[i]); err != nil {
			return nil, errors.Trace(err)
		} else {
			s.AddSet(set)
		}
	}
	return s, nil
}

func DecodeMysqlGTIDSet(data []byte) (*MysqlGTIDSet, error) {
	s := new(MysqlGTIDSet)

	if len(data) < 8 {
		return nil, errors.Errorf("invalid gtid set buffer, less 4")
	}

	n := int(binary.LittleEndian.Uint64(data))
	s.Sets = make(map[string]*UUIDSet, n)

	pos := 8

	for i := 0; i < n; i++ {
		set := new(UUIDSet)
		if n, err := set.decode(data[pos:]); err != nil {
			return nil, errors.Trace(err)
		} else {
			pos += n

			s.AddSet(set)
		}
	}
	return s, nil
}

func (s *MysqlGTIDSet) AddSet(set *UUIDSet) {
	if set == nil {
		return
	}
	sid := set.SID.String()
	o, ok := s.Sets[sid]
	if ok {
		o.AddInterval(set.Intervals)
	} else {
		s.Sets[sid] = set
	}
}

func (s *MysqlGTIDSet) MinusSet(set *UUIDSet) {
	if set == nil {
		return
	}
	sid := set.SID.String()
	uuidSet, ok := s.Sets[sid]
	if ok {
		uuidSet.MinusInterval(set.Intervals)
		if uuidSet.Intervals == nil {
			delete(s.Sets, sid)
		}
	}
}

func (s *MysqlGTIDSet) Update(GTIDStr string) error {
	gtidSet, err := ParseMysqlGTIDSet(GTIDStr)
	if err != nil {
		return err
	}
	for _, uuidSet := range gtidSet.(*MysqlGTIDSet).Sets {
		s.AddSet(uuidSet)
	}
	return nil
}

func (s *MysqlGTIDSet) AddGTID(uuid uuid.UUID, gno int64) {
	sid := uuid.String()
	o, ok := s.Sets[sid]
	if ok {
		o.Intervals.InsertInterval(Interval{gno, gno + 1})
	} else {
		s.Sets[sid] = &UUIDSet{uuid, IntervalSlice{Interval{gno, gno + 1}}}
	}
}

func (s *MysqlGTIDSet) Add(addend MysqlGTIDSet) error {
	for _, uuidSet := range addend.Sets {
		s.AddSet(uuidSet)
	}
	return nil
}

func (s *MysqlGTIDSet) Minus(subtrahend MysqlGTIDSet) error {
	for _, uuidSet := range subtrahend.Sets {
		s.MinusSet(uuidSet)
	}
	return nil
}

func (s *MysqlGTIDSet) Contain(o GTIDSet) bool {
	sub, ok := o.(*MysqlGTIDSet)
	if !ok {
		return false
	}

	for key, set := range sub.Sets {
		o, ok := s.Sets[key]
		if !ok {
			return false
		}

		if !o.Contain(set) {
			return false
		}
	}

	return true
}

func (s *MysqlGTIDSet) Equal(o GTIDSet) bool {
	sub, ok := o.(*MysqlGTIDSet)
	if !ok {
		return false
	}

	if len(sub.Sets) != len(s.Sets) {
		return false
	}

	for key, set := range sub.Sets {
		o, ok := s.Sets[key]
		if !ok {
			return false
		}

		if !o.Intervals.Equal(set.Intervals) {
			return false
		}
	}

	return true
}

func (s *MysqlGTIDSet) String() string {
	// there is only one element in gtid set
	if len(s.Sets) == 1 {
		for _, set := range s.Sets {
			return set.String()
		}
	}

	// sort multi set
	var buf bytes.Buffer
	sets := make([]string, 0, len(s.Sets))
	for _, set := range s.Sets {
		sets = append(sets, set.String())
	}
	sort.Strings(sets)

	sep := ""
	for _, set := range sets {
		buf.WriteString(sep)
		buf.WriteString(set)
		sep = ","
	}

	return utils.ByteSliceToString(buf.Bytes())
}

func (s *MysqlGTIDSet) Encode() []byte {
	var buf bytes.Buffer

	_ = binary.Write(&buf, binary.LittleEndian, uint64(len(s.Sets)))

	for i := range s.Sets {
		s.Sets[i].encode(&buf)
	}

	return buf.Bytes()
}

func (gtid *MysqlGTIDSet) Clone() GTIDSet {
	clone := &MysqlGTIDSet{
		Sets: make(map[string]*UUIDSet),
	}
	for sid, uuidSet := range gtid.Sets {
		clone.Sets[sid] = uuidSet.Clone()
	}

	return clone
}

func (s *MysqlGTIDSet) IsEmpty() bool {
	return len(s.Sets) == 0
}
