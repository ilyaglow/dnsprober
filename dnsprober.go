package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"sync"

	"github.com/ilyaglow/udpspoof"
	"github.com/miekg/dns"
	"github.com/tidwall/evio"
)

var (
	domainsFile = flag.String("i", "domains.txt", "File with domains")
	srcIP = "YOUR-IP"

func main() {
	var wg sync.WaitGroup
	wg.Add(1)
	defer wg.Wait()
	go func() {
		defer wg.Done()
		err := doEvio()
		if err != nil {
			log.Println(err)
		}
	}()

	conn, err := udpspoof.NewUDPConn("8.8.8.8:53")
	if err != nil {
		log.Fatal(err)
	}

	f, err := os.Open(*domainsFile)
	if err != nil {
		log.Fatal(err)
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		m := new(dns.Msg)
		m.SetQuestion(fmt.Sprintf("%s.", scanner.Text()), dns.TypeA)
		b, err := m.Pack()
		if err != nil {
			log.Fatal(err)
		}

		_, err = conn.WriteAs(net.ParseIP(srcIP), b)
		if err != nil {
			log.Fatal(err)
		}
	}
}

func doEvio() error {
	var events evio.Events
	events.Data = func(c evio.Conn, in []byte) (out []byte, action evio.Action) {
		log.Println(c.RemoteAddr())

		m := new(dns.Msg)
		err := m.Unpack(in)
		if err != nil {
			log.Println(err)
		}

		log.Println(m)
		return
	}
	return evio.Serve(events, "udp://:54321?reuseport=true")
}
