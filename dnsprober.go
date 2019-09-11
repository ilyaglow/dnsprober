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
	waitSecs      = flag.Int("t", 5, "Seconds to wait for incoming replies")
	throttleMSecs = flag.Int("throttle", 10, "Microseconds to wait before sending new packet to socket")
	rTypes        = flag.String("q", "A,AAAA,NS,MX,SOA,SRV", "Record types to query")
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

	ticker := time.NewTicker(time.Duration(*throttleMSecs) * time.Microsecond)
	types := csvToDNSTypes(*rTypes)

	scanner = bufio.NewScanner(f)
	for scanner.Scan() {
		select {
		case <-cancel:
			break
		default:
			for i := range types {
				m := new(dns.Msg)
				m.SetQuestion(fmt.Sprintf("%s.", scanner.Text()), types[i])
				b, err := m.Pack()
				if err != nil {
					log.Fatal(err)
				}

				resolver := rotatedResolver()
				_, err = conn.WriteTo(b, resolver)
				if err != nil {
					log.Fatal(err)
				}
				<-ticker.C
			}
		}
	}

	log.Printf("waiting %d seconds...", *waitSecs)
	time.Sleep(time.Duration(*waitSecs) * time.Second)
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
// Resolvers should be in the form: ip or ip:port. Only UDP resolvers are supported.
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

func csvToDNSTypes(s string) []uint16 {
	var types []uint16
	qs := strings.Split(s, ",")

	mm := make(map[string]uint16, len(dns.TypeToString))
	for k, v := range dns.TypeToString {
		mm[v] = k
	}

	for _, q := range qs {
		types = append(types, mm[q])
	}

	return types
}
