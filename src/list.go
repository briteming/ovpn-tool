// list.go -- list one or many user certs
//
// (c) 2018 Sudhi Herle; License GPLv2
//
// This software does not come with any express or implied
// warranty; it is provided "as is". No claim  is made to its
// suitability for any purpose.

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/opencoff/ovpn-tool/pki"
	flag "github.com/opencoff/pflag"
)

func ListCert(db string, args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	fs.Usage = func() {
		listUsage(fs)
	}

	var showCA bool

	fs.BoolVarP(&showCA, "ca", "", false, "Display the CA certificate")

	err := fs.Parse(args)
	if err != nil {
		die("%s", err)
	}

	ca := OpenCA(db)
	defer ca.Close()

	if showCA {
		fmt.Printf("CA Certificate:\n%s\n", Cert(*ca.Crt))
	}

	args = fs.Args()

	if len(args) == 0 {
		// always print the abbreviated root-CA
		c := &pki.Cert{
			Crt: ca.Crt,
		}
		printcert(c, true)

		ca.MapCA(func(c *pki.Cert) error {
			printcert(c, false)
			return nil
		})

		ca.MapServers(func(c *pki.Cert) error {
			printcert(c, false)
			return nil
		})

		ca.MapUsers(func(c *pki.Cert) error {
			printcert(c, false)
			return nil
		})

		return
	}

	for _, cn := range args {
		c, err := ca.Find(cn)
		if err != nil {
			warn("Can't find Common Name %s", cn)
			continue
		}
		printcert(c, false)
	}
}

func printcert(c *pki.Cert, rootCA bool) {
	var pref, server string
	z := c.Crt

	now := time.Now().UTC()
	if now.After(z.NotAfter) {
		pref = fmt.Sprintf("EXPIRED %s", z.NotAfter)
	} else {
		pref = fmt.Sprintf("valid until %s", z.NotAfter)
	}

	if c.IsServer {
		server = "server"
	} else if c.IsCA {
		server = "CA (I)"
	} else if rootCA {
		server = "Root-CA"
	}

	fmt.Printf("%-16s  %7.7s %#x (%s)\n", z.Subject.CommonName, server, z.SerialNumber, pref)
	Print("%s\n", Cert(*z))
}

func listUsage(fs *flag.FlagSet) {
	fmt.Printf(`%s list: List one or more issued certificates

Usage: %s DB list [options] [CN...]

Where 'DB' is the CA Database file and 'CN' is common name in the certificate.

Options:
`, os.Args[0], os.Args[0])

	fs.PrintDefaults()
	os.Exit(0)
}
