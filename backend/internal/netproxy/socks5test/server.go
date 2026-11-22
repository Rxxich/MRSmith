// Package socks5test runs a tiny SOCKS5 server for tests.
package socks5test

import (
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
)

type Server struct {
	Addr string
	// User and Pass, when set, require username/password authentication.
	User, Pass string
	// Route maps the requested target to the address actually dialled.
	Route func(target string) string

	mu      sync.Mutex
	targets []string
	ln      net.Listener
}

// Start listens on 127.0.0.1 until the test ends.
func Start(t testing.TB, user, pass string, route func(string) string) *Server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Addr: ln.Addr().String(), User: user, Pass: pass, Route: route, ln: ln}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.handle(c)
		}
	}()
	return s
}

// Targets lists the host:port values clients asked for, in order.
func (s *Server) Targets() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.targets...)
}

func (s *Server) handle(c net.Conn) {
	defer c.Close()
	buf := make([]byte, 262)
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 5 {
		return
	}
	methods := make([]byte, buf[1])
	if _, err := io.ReadFull(c, methods); err != nil {
		return
	}
	if s.User != "" {
		c.Write([]byte{5, 2})
		if _, err := io.ReadFull(c, buf[:2]); err != nil {
			return
		}
		user := make([]byte, buf[1])
		io.ReadFull(c, user)
		io.ReadFull(c, buf[:1])
		pass := make([]byte, buf[0])
		io.ReadFull(c, pass)
		if string(user) != s.User || string(pass) != s.Pass {
			c.Write([]byte{1, 1})
			return
		}
		c.Write([]byte{1, 0})
	} else {
		c.Write([]byte{5, 0})
	}
	if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[1] != 1 {
		return
	}
	var host string
	switch buf[3] {
	case 1:
		ip := make([]byte, 4)
		io.ReadFull(c, ip)
		host = net.IP(ip).String()
	case 4:
		ip := make([]byte, 16)
		io.ReadFull(c, ip)
		host = net.IP(ip).String()
	case 3:
		io.ReadFull(c, buf[:1])
		name := make([]byte, buf[0])
		io.ReadFull(c, name)
		host = string(name)
	default:
		return
	}
	io.ReadFull(c, buf[:2])
	target := net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(buf[:2]))))
	s.mu.Lock()
	s.targets = append(s.targets, target)
	s.mu.Unlock()
	dst := target
	if s.Route != nil {
		dst = s.Route(target)
	}
	up, err := net.Dial("tcp", dst)
	if err != nil {
		c.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer up.Close()
	c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	done := make(chan struct{}, 2)
	go func() { io.Copy(up, c); done <- struct{}{} }()
	go func() { io.Copy(c, up); done <- struct{}{} }()
	<-done
}
