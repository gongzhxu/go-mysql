package dump

import (
	"flag"

	"github.com/gongzhxu/go-mysql/mysql"
)

var execution = flag.String("exec", "mysqldump", "mysqldump execution path")

type testParseHandler struct {
	gset mysql.GTIDSet
}

func (h *testParseHandler) BinLog(name string, pos uint64) error {
	return nil
}

func (h *testParseHandler) GtidSet(gtidsets string) (err error) {
	if h.gset != nil {
		err = h.gset.Update(gtidsets)
	} else {
		h.gset, err = mysql.ParseGTIDSet("mysql", gtidsets)
	}
	return err
}

func (h *testParseHandler) Data(schema string, table string, values []string) error {
	return nil
}
