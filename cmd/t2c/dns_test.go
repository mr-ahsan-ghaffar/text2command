package main

import (
	"context"
	"debug/buildinfo"
	"encoding/binary"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// brokenDNS imitates a forwarder like VMware's NAT: plain queries work, but
// a query carrying EDNS gets a reply that can't be parsed.
func brokenDNS(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := conn.ReadFrom(buf)
			if err != nil {
				return
			}
			conn.WriteTo(dnsReply(buf[:n]), addr)
		}
	}()
	return conn.LocalAddr().String()
}

func dnsReply(q []byte) []byte {
	end := 12
	for q[end] != 0 {
		end += int(q[end]) + 1
	}
	end += 5 // Root label, type, and class.
	question := q[12:end]
	qtype := binary.BigEndian.Uint16(q[end-4:])
	hasEDNS := binary.BigEndian.Uint16(q[10:]) > 0

	r := append([]byte{}, q[:2]...) // ID.
	r = append(r, 0x81, 0x80)       // Response, recursion available.
	r = append(r, 0, 1)             // One question.
	answers := byte(0)
	if qtype == 1 {
		answers = 1
	}
	if hasEDNS {
		answers = 3 // More records are promised than the packet holds.
	}
	r = append(r, 0, answers, 0, 0, 0, 0)
	r = append(r, question...)
	if qtype == 1 && !hasEDNS {
		r = append(r, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4, 172, 64, 146, 15)
	}
	return r
}

func lookup(server string) ([]string, error) {
	r := &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", server)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return r.LookupHost(ctx, "auth.openai.com")
}

func TestLookupWorksWithBrokenEDNS(t *testing.T) {
	addrs, err := lookup(brokenDNS(t))
	wantFailure := os.Getenv("T2C_TEST_EDNS_ON") == "1"
	if wantFailure {
		// Run below with EDNS on, where this server must break the lookup. If it
		// didn't, this test wouldn't be proving anything.
		if err == nil {
			t.Fatalf("the fake server didn't break a lookup that used EDNS: %v", addrs)
		}
		return
	}
	if err != nil {
		t.Fatalf("lookup failed: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != "172.64.146.15" {
		t.Errorf("got %v", addrs)
	}
}

func TestFakeServerBreaksEDNS(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestLookupWorksWithBrokenEDNS$")
	cmd.Env = append(os.Environ(), "GODEBUG=netedns0=1", "T2C_TEST_EDNS_ON=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}

// Tests inherit the setting from main.go. This checks that the built program has it too.
func TestProgramTurnsOffEDNS(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "t2c")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	info, err := buildinfo.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range info.Settings {
		if s.Key == "DefaultGODEBUG" && strings.Contains(s.Value, "netedns0=0") {
			return
		}
	}
	t.Errorf("the program doesn't turn off EDNS: %v", info.Settings)
}
