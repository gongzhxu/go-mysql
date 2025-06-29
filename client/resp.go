package client

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"

	"github.com/pingcap/errors"

	"github.com/gongzhxu/go-mysql/mysql"
	"github.com/gongzhxu/go-mysql/utils"
)

func (c *Conn) readUntilEOF() (err error) {
	var data []byte

	for {
		data, err = c.ReadPacket()
		if err != nil {
			return
		}

		// EOF Packet
		if c.isEOFPacket(data) {
			return
		}
	}
}

func (c *Conn) isEOFPacket(data []byte) bool {
	return data[0] == mysql.EOF_HEADER && len(data) <= 5
}

func (c *Conn) handleOKPacket(data []byte) (*mysql.Result, error) {
	var n int
	pos := 1

	r := mysql.NewResultReserveResultset(0)

	r.AffectedRows, _, n = mysql.LengthEncodedInt(data[pos:])
	pos += n
	r.InsertId, _, n = mysql.LengthEncodedInt(data[pos:])
	pos += n

	if c.capability&mysql.CLIENT_PROTOCOL_41 > 0 {
		r.Status = binary.LittleEndian.Uint16(data[pos:])
		c.status = r.Status
		pos += 2

		//todo:strict_mode, check warnings as error
		r.Warnings = binary.LittleEndian.Uint16(data[pos:])
		// pos += 2
	} else if c.capability&mysql.CLIENT_TRANSACTIONS > 0 {
		r.Status = binary.LittleEndian.Uint16(data[pos:])
		c.status = r.Status
		// pos += 2
	}

	// new ok package will check CLIENT_SESSION_TRACK too, but I don't support it now.

	// skip info
	return r, nil
}

func (c *Conn) handleErrorPacket(data []byte) error {
	e := new(mysql.MyError)

	pos := 1

	e.Code = binary.LittleEndian.Uint16(data[pos:])
	pos += 2

	if c.capability&mysql.CLIENT_PROTOCOL_41 > 0 {
		// skip '#'
		pos++
		e.State = utils.ByteSliceToString(data[pos : pos+5])
		pos += 5
	}

	e.Message = utils.ByteSliceToString(data[pos:])

	return e
}

func (c *Conn) handleAuthResult() error {
	data, switchToPlugin, err := c.readAuthResult()
	if err != nil {
		return fmt.Errorf("readAuthResult: %w", err)
	}
	// handle auth switch, only support 'sha256_password', and 'caching_sha2_password'
	if switchToPlugin != "" {
		// fmt.Printf("now switching auth plugin to '%s'\n", switchToPlugin)
		if data == nil {
			data = c.salt
		} else {
			copy(c.salt, data)
		}
		c.authPluginName = switchToPlugin
		auth, addNull, err := c.genAuthResponse(data)
		if err != nil {
			return err
		}

		if err = c.WriteAuthSwitchPacket(auth, addNull); err != nil {
			return err
		}

		// Read Result Packet
		data, switchToPlugin, err = c.readAuthResult()
		if err != nil {
			return err
		}

		// Do not allow to change the auth plugin more than once
		if switchToPlugin != "" {
			return errors.Errorf("can not switch auth plugin more than once")
		}
	}

	// handle caching_sha2_password
	switch c.authPluginName {
	case mysql.AUTH_CACHING_SHA2_PASSWORD:
		if data == nil {
			return nil // auth already succeeded
		}
		switch data[0] {
		case mysql.CACHE_SHA2_FAST_AUTH:
			_, err = c.readOK()
			return err
		case mysql.CACHE_SHA2_FULL_AUTH:
			// need full authentication
			if c.tlsConfig != nil || c.proto == "unix" {
				if err = c.WriteClearAuthPacket(c.password); err != nil {
					return err
				}
			} else {
				if err = c.WritePublicKeyAuthPacket(c.password, c.salt); err != nil {
					return err
				}
			}
			_, err = c.readOK()
			return err
		default:
			return errors.Errorf("invalid packet %x", data[0])
		}
	case mysql.AUTH_SHA256_PASSWORD:
		if len(data) == 0 {
			return nil // auth already succeeded
		}
		block, _ := pem.Decode(data)
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return err
		}
		// send encrypted password
		err = c.WriteEncryptedPassword(c.password, c.salt, pub.(*rsa.PublicKey))
		if err != nil {
			return err
		}
		_, err = c.readOK()
		return err
	}
	return nil
}

func (c *Conn) readAuthResult() ([]byte, string, error) {
	data, err := c.ReadPacket()
	if err != nil {
		return nil, "", fmt.Errorf("ReadPacket: %w", err)
	}

	// see: https://insidemysql.com/preparing-your-community-connector-for-mysql-8-part-2-sha256/
	// packet indicator
	switch data[0] {
	case mysql.OK_HEADER:
		_, err := c.handleOKPacket(data)
		return nil, "", err

	case mysql.MORE_DATE_HEADER:
		return data[1:], "", err

	case mysql.EOF_HEADER:
		// server wants to switch auth
		if len(data) < 1 {
			// https://dev.mysql.com/doc/internals/en/connection-phase-packets.html#packet-Protocol::OldAuthSwitchRequest
			return nil, mysql.AUTH_MYSQL_OLD_PASSWORD, nil
		}
		pluginEndIndex := bytes.IndexByte(data, 0x00)
		if pluginEndIndex < 0 {
			return nil, "", errors.New("invalid packet")
		}
		plugin := string(data[1:pluginEndIndex])
		authData := data[pluginEndIndex+1:]
		return authData, plugin, nil

	default: // Error otherwise
		return nil, "", c.handleErrorPacket(data)
	}
}

func (c *Conn) readOK() (*mysql.Result, error) {
	data, err := c.ReadPacket()
	if err != nil {
		return nil, errors.Trace(err)
	}

	switch data[0] {
	case mysql.OK_HEADER:
		return c.handleOKPacket(data)
	case mysql.ERR_HEADER:
		return nil, c.handleErrorPacket(data)
	default:
		return nil, errors.New("invalid ok packet")
	}
}

func (c *Conn) readResult(binary bool) (*mysql.Result, error) {
	bs := utils.ByteSliceGet(16)
	defer utils.ByteSlicePut(bs)
	var err error
	bs.B, err = c.ReadPacketReuseMem(bs.B[:0])
	if err != nil {
		return nil, errors.Trace(err)
	}

	switch bs.B[0] {
	case mysql.OK_HEADER:
		return c.handleOKPacket(bs.B)
	case mysql.ERR_HEADER:
		return nil, c.handleErrorPacket(bytes.Repeat(bs.B, 1))
	case mysql.LocalInFile_HEADER:
		return nil, mysql.ErrMalformPacket
	default:
		return c.readResultset(bs.B, binary)
	}
}

func (c *Conn) readResultStreaming(binary bool, result *mysql.Result, perRowCb SelectPerRowCallback, perResCb SelectPerResultCallback) error {
	bs := utils.ByteSliceGet(16)
	defer utils.ByteSlicePut(bs)
	var err error
	bs.B, err = c.ReadPacketReuseMem(bs.B[:0])
	if err != nil {
		return errors.Trace(err)
	}

	switch bs.B[0] {
	case mysql.OK_HEADER:
		// https://dev.mysql.com/doc/internals/en/com-query-response.html
		// 14.6.4.1 COM_QUERY Response
		// If the number of columns in the resultset is 0, this is a OK_Packet.

		okResult, err := c.handleOKPacket(bs.B)
		if err != nil {
			return errors.Trace(err)
		}

		result.Status = okResult.Status
		result.AffectedRows = okResult.AffectedRows
		result.InsertId = okResult.InsertId
		result.Warnings = okResult.Warnings
		if result.Resultset == nil {
			result.Resultset = mysql.NewResultset(0)
		} else {
			result.Reset(0)
		}
		return nil
	case mysql.ERR_HEADER:
		return c.handleErrorPacket(bytes.Repeat(bs.B, 1))
	case mysql.LocalInFile_HEADER:
		return mysql.ErrMalformPacket
	default:
		return c.readResultsetStreaming(bs.B, binary, result, perRowCb, perResCb)
	}
}

func (c *Conn) readResultset(data []byte, binary bool) (*mysql.Result, error) {
	// column count
	count, _, n := mysql.LengthEncodedInt(data)

	if n-len(data) != 0 {
		return nil, mysql.ErrMalformPacket
	}

	result := mysql.NewResultReserveResultset(int(count))

	if err := c.readResultColumns(result); err != nil {
		return nil, errors.Trace(err)
	}

	if err := c.readResultRows(result, binary); err != nil {
		return nil, errors.Trace(err)
	}

	return result, nil
}

func (c *Conn) readResultsetStreaming(data []byte, binary bool, result *mysql.Result, perRowCb SelectPerRowCallback, perResCb SelectPerResultCallback) error {
	columnCount, _, n := mysql.LengthEncodedInt(data)

	if n-len(data) != 0 {
		return mysql.ErrMalformPacket
	}

	if result.Resultset == nil {
		result.Resultset = mysql.NewResultset(int(columnCount))
	} else {
		// Reuse memory if can
		result.Reset(int(columnCount))
	}

	// this is a streaming resultset
	result.Streaming = mysql.StreamingSelect

	if err := c.readResultColumns(result); err != nil {
		return errors.Trace(err)
	}

	if perResCb != nil {
		if err := perResCb(result); err != nil {
			return err
		}
	}

	if err := c.readResultRowsStreaming(result, binary, perRowCb); err != nil {
		return errors.Trace(err)
	}

	// this resultset is done streaming
	result.StreamingDone = true

	return nil
}

func (c *Conn) readResultColumns(result *mysql.Result) (err error) {
	i := 0
	var data []byte

	for {
		rawPkgLen := len(result.RawPkg)
		result.RawPkg, err = c.ReadPacketReuseMem(result.RawPkg)
		if err != nil {
			return err
		}
		data = result.RawPkg[rawPkgLen:]

		// EOF Packet
		if c.isEOFPacket(data) {
			if c.capability&mysql.CLIENT_PROTOCOL_41 > 0 {
				result.Warnings = binary.LittleEndian.Uint16(data[1:])
				// todo add strict_mode, warning will be treat as error
				result.Status = binary.LittleEndian.Uint16(data[3:])
				c.status = result.Status
			}

			if i != len(result.Fields) {
				err = mysql.ErrMalformPacket
			}

			return err
		}

		if result.Fields[i] == nil {
			result.Fields[i] = &mysql.Field{}
		}
		err = result.Fields[i].Parse(data)
		if err != nil {
			return err
		}

		result.FieldNames[utils.ByteSliceToString(result.Fields[i].Name)] = i

		i++
	}
}

func (c *Conn) readResultRows(result *mysql.Result, isBinary bool) (err error) {
	var data []byte

	for {
		rawPkgLen := len(result.RawPkg)
		result.RawPkg, err = c.ReadPacketReuseMem(result.RawPkg)
		if err != nil {
			return err
		}
		data = result.RawPkg[rawPkgLen:]

		// EOF Packet
		if c.isEOFPacket(data) {
			if c.capability&mysql.CLIENT_PROTOCOL_41 > 0 {
				result.Warnings = binary.LittleEndian.Uint16(data[1:])
				// todo add strict_mode, warning will be treat as error
				result.Status = binary.LittleEndian.Uint16(data[3:])
				c.status = result.Status
			}

			break
		}

		if data[0] == mysql.ERR_HEADER {
			return c.handleErrorPacket(data)
		}

		result.RowDatas = append(result.RowDatas, data)
	}

	if cap(result.Values) < len(result.RowDatas) {
		result.Values = make([][]mysql.FieldValue, len(result.RowDatas))
	} else {
		result.Values = result.Values[:len(result.RowDatas)]
	}

	for i := range result.Values {
		result.Values[i], err = result.RowDatas[i].Parse(result.Fields, isBinary, result.Values[i])
		if err != nil {
			return errors.Trace(err)
		}
	}

	return nil
}

func (c *Conn) readResultRowsStreaming(result *mysql.Result, isBinary bool, perRowCb SelectPerRowCallback) (err error) {
	var (
		data []byte
		row  []mysql.FieldValue
	)

	for {
		data, err = c.ReadPacketReuseMem(data[:0])
		if err != nil {
			return err
		}

		// EOF Packet
		if c.isEOFPacket(data) {
			if c.capability&mysql.CLIENT_PROTOCOL_41 > 0 {
				result.Warnings = binary.LittleEndian.Uint16(data[1:])
				// todo add strict_mode, warning will be treat as error
				result.Status = binary.LittleEndian.Uint16(data[3:])
				c.status = result.Status
			}

			break
		}

		if data[0] == mysql.ERR_HEADER {
			return c.handleErrorPacket(data)
		}

		// Parse this row
		row, err = mysql.RowData(data).Parse(result.Fields, isBinary, row)
		if err != nil {
			return errors.Trace(err)
		}

		// Send the row to "userland" code
		err = perRowCb(row)
		if err != nil {
			return errors.Trace(err)
		}
	}

	return nil
}
