package simk

import (
	"fmt"
	"log"
	"net"
	"os/exec"
	"pryct1/req"
	"sync"
)

type clientReq struct {
	r  req.Req
	cl clientConn
}

var conns sync.Map //, clientConn

var (
	enter_ch = make(chan net.Conn)
	state_ch = make(chan clientReq)
)

const (
	maxOpenProcs = 10
	appClientCMD = "st -e go run acmain.go"
	fmClientCMD  = "st -e go run fmmain.go"
)

func remClient(cind uint32) {
	c_raw, found := conns.LoadAndDelete(cind)
	if found {
		c := c_raw.(clientConn)
		c.Conn.Close()
	}
}

func logMsg(orig req.Req, payload []byte) bool {
	//omits OK, ERR, IDEN
	if orig.Rtype == req.OK || orig.Rtype == req.ERR ||
		orig.Rtype == req.IDEN {
		return false
	}

	c_raw, found := conns.Load(uint32(1))
	if found {
		c := c_raw.(clientConn)
		logbuf := make([]byte, req.ReqBufSize)

		resp := req.Req{
			Rtype: req.LOGMSG,
			Info:  1,
			Plsz:  uint32(req.ReqBufSize + len(payload)),
		}

		req.ReqSerial(logbuf, &resp)
		c.Conn.Write(logbuf)

		req.ReqSerial(logbuf, &orig)
		logbuf = append(logbuf, payload...)
		c.Conn.Write(logbuf)

		return true
	}

	return false
}

func sendMsg(c clientConn, rtype uint16, payload []byte,
	src uint32) bool {

	reqbuf := make([]byte, req.ReqBufSize)
	resp := req.Req{
		Rtype: rtype,
		Src:   src,
		Info:  c.ClientID,
		Plsz:  uint32(len(payload)),
	}

	req.ReqSerial(reqbuf, &resp)
	_, err1 := c.Conn.Write(reqbuf)
	_, err2 := c.Conn.Write(payload)

	//ignore OK's and ERR's, too many of em
	logMsg(resp, payload)

	if err1 != nil || err2 != nil {
		fmt.Println("error sending message")
		return false
	}

	return true
}

func fwdMsg(c clientConn, r req.Req) {
	plbuf := make([]byte, r.Plsz)
	read, err := c.Conn.Read(plbuf)

	if err != nil || read == 0 {
		remClient(c.ClientID)
		return
	}

	dst_raw, dstinmap := conns.Load(r.Info)
	if dst_raw == nil || !dstinmap {
		msg := fmt.Sprintf("dst %d doesnt exist", r.Info)
		sendMsg(c, req.ERR, []byte(msg), 0)
	} else {
		dst := dst_raw.(clientConn)
		sent := sendMsg(dst, req.FWDMSG, plbuf, c.ClientID)
		if !sent {
			msg := fmt.Sprintf("cant send msg to %d", r.Info)
			sendMsg(c, req.ERR, []byte(msg), 0)
		} else {
			msg := fmt.Sprintf("sent message to %d", r.Info)
			sendMsg(c, req.OK, []byte(msg), 0)
		}
	}
}

func clientHandler(cind uint32) {
	fmt.Printf("started clientHandler %d\n", cind)
	reqbuf := make([]byte, req.ReqBufSize)

	for {
		c_raw, srcinmap := conns.Load(cind)
		if c_raw == nil || !srcinmap {
			remClient(cind)
			break
		}

		c := c_raw.(clientConn)
		read, err := c.Conn.Read(reqbuf)
		if err != nil || read == 0 {
			remClient(cind)
			break
		}

		var creq req.Req
		req.ReqDeserial(&creq, reqbuf)
		fmt.Printf("req recieved %s\n", creq)

		switch {
		case (creq.Rtype >= req.PROPEN &&
			creq.Rtype <= req.FMOPEN):
			state_ch <- clientReq{creq, c}

		case creq.Rtype == req.FWDMSG:
			fwdMsg(c, creq)
		}
	}

	fmt.Printf("closing clientHandler %d\n", cind)
}

func openProc(openProcs *uint32) (uint16, []byte) {
	if *(openProcs) >= maxOpenProcs {
		msg := fmt.Sprintf("cant open anymore processes")
		return req.ERR, []byte(msg)
	}

	cmd := exec.Command("sh", "-c", appClientCMD)
	if err := cmd.Start(); err != nil {
		msg := fmt.Sprintf("couldnt start process")
		return req.ERR, []byte(msg)
	}

	*openProcs++

	msg := fmt.Sprintf("process number %d started", *openProcs)
	return req.OK, []byte(msg)
}

func closeProc(openProcs *uint32, procID uint32,
	fmOpen *bool) (uint16, []byte) {

	if procID < 1 {
		msg := fmt.Sprintf("invalid process ID %d", procID)
		return req.ERR, []byte(msg)
	}

	c_raw, inmap := conns.Load(procID)
	if !inmap {
		msg := fmt.Sprintf("process %d not found", procID)
		return req.ERR, []byte(msg)
	}

	c := c_raw.(clientConn)
	if c.Ctype == req.USER {
		msg := fmt.Sprintf("client %d is a fellow user", procID)
		return req.ERR, []byte(msg)
	}
	sendMsg(c, req.PRCLOSE, []byte{}, 0)

	*openProcs--
	if procID == 1 {
		*fmOpen = false
	}

	msg := fmt.Sprintf("process %d closed", procID)
	return req.OK, []byte(msg)
}

func listProc() (uint16, []byte) {
	proclist := []byte{}

	fn := func(key, value interface{}) bool {
		cind := key.(uint32)
		c := value.(clientConn)

		tmpSlice := make([]byte, 4+1)
		req.SerU32(cind, tmpSlice)
		tmpSlice[4] = byte(c.Ctype)

		proclist = append(proclist, tmpSlice...)

		return true
	}

	conns.Range(fn)
	return req.PRLIST, proclist
}

func openFM(fmOpen *bool) (uint16, []byte) {
	if *fmOpen {
		msg := "file manager is already open"
		return req.ERR, []byte(msg)
	}

	cmd := exec.Command("sh", "-c", fmClientCMD)
	if err := cmd.Start(); err != nil {
		msg := fmt.Sprintf("couldnt start file manager")
		return req.ERR, []byte(msg)
	}

	*fmOpen = true
	msg := "file manager opened succesfully"
	return req.OK, []byte(msg)
}

func stateHandler() {
	var openProcs uint32 = 0
	fmOpen := false

	for clreq := range state_ch {
		var succ uint16
		var msg []byte

		switch clreq.r.Rtype {
		case req.PROPEN:
			fmt.Println("opening process")
			succ, msg = openProc(&openProcs)
		case req.PRCLOSE:
			fmt.Println("closing process")
			succ, msg = closeProc(&openProcs, clreq.r.Info, &fmOpen)

		case req.PRLIST:
			fmt.Println("listing processes")
			succ, msg = listProc()
		case req.FMOPEN:
			fmt.Println("opening file manager")
			succ, msg = openFM(&fmOpen)

		}
		// log old message
		logMsg(clreq.r, []byte{})
		sendMsg(clreq.cl, succ, msg, 0)
	}

	fmt.Println("closin stateHandler")
}

func handleEnter() {
	var ind uint32 = 2
	reqbuf := make([]byte, req.ReqBufSize)
	var idenReq req.Req

	for nconn := range enter_ch {
		newcc := clientConn{Conn: nconn, ClientID: ind}
		sendMsg(newcc, req.OK, []byte{}, 0) //send it its new ID

		read, err := nconn.Read(reqbuf) //read IDEN response
		req.ReqDeserial(&idenReq, reqbuf)

		if err == nil && read > 0 &&
			idenReq.Rtype == req.IDEN {

			newcc.Ctype = uint8(idenReq.Info)

			// you dont check if fm is already open.
			// this can result in multiple file managers
			sendMsg(newcc, req.OK, []byte{}, 0)
			if newcc.Ctype == req.FM {
				newcc.ClientID = 1
			}

			fmt.Printf("client %d claims to be a %s\n",
				newcc.ClientID, req.CtypeMap[newcc.Ctype])

			conns.Store(newcc.ClientID, newcc)
			go clientHandler(newcc.ClientID)
			ind++
		}

	}

	fmt.Println("closin handleEnter")
}

func StartSimK() {
	listenAddr := "localhost"
	listenPort := "8000"
	listener, err := net.Listen("tcp", listenAddr+":"+listenPort)
	if err != nil {
		log.Fatal(err)
	}

	go handleEnter()
	go stateHandler()

	fmt.Println("starting simk bb")
	for {
		client, err := listener.Accept()

		if err != nil {
			log.Print(err)
			continue
		}

		enter_ch <- client
	}

}
