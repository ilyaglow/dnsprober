package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ilyaglow/evio"
	"github.com/miekg/dns"
)

var (
	domainsFile   = flag.String("i", "domains.txt", "File with domains")
	resolversFile = flag.String("r", "resolvers.txt", "File with resolvers")
)

func main() {
	flag.Parse()

	conn, err := net.ListenUDP("udp", nil)
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	go func() {
		err := doEvio(conn)
		if err != nil {
			log.Fatal(err)
		}
	}()

	r, err := os.Open(*resolversFile)
	if err != nil {
		log.Fatal(err)
	}

	var resolvers []string
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		resolvers = append(resolvers, scanner.Text())
	}

	rotatedResolver, err := NewResolvers(resolvers)
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Open(*domainsFile)
	if err != nil {
		log.Fatal(err)
	}

	cancel := make(chan os.Signal, 1)
	signal.Notify(cancel, syscall.SIGINT, syscall.SIGTERM)

	ticker := time.NewTicker(1 * time.Millisecond)

	scanner = bufio.NewScanner(f)
	for scanner.Scan() {
		select {
		case <-ticker.C:
			m := new(dns.Msg)
			m.SetQuestion(fmt.Sprintf("%s.", scanner.Text()), dns.TypeA)
			b, err := m.Pack()
			if err != nil {
				log.Fatal(err)
			}

			resolver := rotatedResolver()
			_, err = conn.WriteTo(b, resolver)
			if err != nil {
				log.Fatal(err)
			}
		case <-cancel:
			break
		}
	}

	log.Println("waiting 15 seconds...")
	time.Sleep(15 * time.Second)
}

func doEvio(conn *net.UDPConn) error {
	var events evio.Events
	events.Data = func(c evio.Conn, in []byte) (out []byte, action evio.Action) {
		m := new(dns.Msg)
		err := m.Unpack(in)
		if err != nil {
			log.Println(err)
		}

		fmt.Println(m)
		return
	}
	return evio.ServePacketConn(events, conn)
}

// NewResolvers return a function for rotating resolvers in round-robin fashion
// or a parsing error.
// Resolvers should be in the form: ip:port. Only UDP resolvers are supported.
func NewResolvers(rs []string) (func() net.Addr, error) {
	var (
		addrs []*net.UDPAddr
		c     int
	)
	for i := range rs {
		parts = strings.Split(rs[i], ":")
		if len(parts) == 1 {
			rs[i] = rs[i] + ":53"
		}

		a, err := net.ResolveUDPAddr("udp", rs[i])
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, a)
	}

	return func() net.Addr {
		defer func() { c++ }()
		if c == len(addrs) {
			c = 0
		}
		return addrs[c]
	}, nil
}
