// del.go -- Delete one or more users
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

	flag "github.com/opencoff/pflag"
)

func DelUser(db string, args []string) {
	fs := flag.NewFlagSet("delete", flag.ExitOnError)
	fs.Usage = func() {
		delUsage(fs)
	}

	err := fs.Parse(args)
	if err != nil {
		die("%s", err)
	}

	args = fs.Args()
	if len(args) < 1 {
		warn("Insufficient arguments to 'delete'\n")
		fs.Usage()
	}

	ca := OpenCA(db)
	defer ca.Close()

	gone := 0

	for _, cn := range args {
		err := ca.DeleteUser(cn)
		if err != nil {
			warn("%s\n")
		} else {
			gone++
			Print("Deleted user %s ..\n", cn)
		}
	}

	if gone > 0 {
		fmt.Printf("Don't forget to generate a new CRL (%s %s crl)\n", os.Args[0], db)
	}
}

func delUsage(fs *flag.FlagSet) {
	fmt.Printf(`%s delete: Delete one or more users ..

Usage: %s DB delete [options] CN [CN...]

Where 'DB' is the CA Database file name and 'CN' is the CommonName for the server

Options:
`, os.Args[0], os.Args[0])

	fs.PrintDefaults()
	os.Exit(0)
}
