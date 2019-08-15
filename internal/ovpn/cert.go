// cert.go - opinionated pki for openvpn
//
// (c) 2018 Sudhi Herle; License GPLv2
//
// This software does not come with any express or implied
// warranty; it is provided "as is". No claim  is made to its
// suitability for any purpose.

// Package ovpn abstracts creating an opinionated PKI for OpenVPN
// The certs and keys are stored in a boltDB instance. The private keys
// are stored in encrypted form. The CA passphrase is used in a KDF to derive
// the encryption keys. User (client) certs are also encrypted - but with
// user provided passphrase.
package ovpn

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"time"
)

// CA is a special type of Credential that also has a CSR in it.
type CA struct {
	Crt    *x509.Certificate
	privKey *ecdsa.PrivateKey
	serial  *big.Int

	db *database

	passwd   string
}

// Cert represents a client or server certificate
type Cert struct {
	Crt    *x509.Certificate
	Key    *ecdsa.PrivateKey
	Rawkey []byte
}

// Server Info
type ServerInfo struct {
	// External VPN IP:port (server endpoint)
	IP   net.IP
	Port uint16

	// DoS protection via --tls-crypt
	// This is a 2048 bit mutually shared key
	TLS []byte
}

// Server data
type Server struct {
	Cert
	ServerInfo
}

// Return the certificate & Key in PEM format.
// The key may be encrypted (if the cert & key were initially protected
// by a passphrase).
func (c *Cert) PEM() (crt []byte, key []byte) {
	p := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: c.Crt.Raw,
	}

	crt = pem.EncodeToMemory(p)
	key = c.Rawkey
	return
}

// CAparams holds the initial info needed to setup a CA
type CAparams struct {
	// Passphrase to encrypt the CA credentials
	Passwd   string
	Subject  pkix.Name
	Validity time.Duration

	// Ask user for a password
	NoPasswd bool

	// DB file where CA details, CRL, etc are stored
	// This is a boltDB instance
	DBfile string

	// If set, create the DB when it is missing
	CreateIfMissing bool
}

// Create or Open a CA instance using the parameters in 'p'
func NewCA(p *CAparams) (*CA, error) {

	d, err := newDB(p.DBfile, p.Passwd, p.CreateIfMissing)
	if err != nil {
		return nil, err
	}

	cd, err := d.getCA(p.Passwd)
	if err != nil {
		return nil, err
	}

	// If no CA, then we create if needed
	if cd == nil {
		if len(p.Subject.CommonName) == 0 {
			return nil, fmt.Errorf("CA CommonName cannot be empty!")
		}

		if !p.CreateIfMissing {
			panic("create-if-missing check bad")
		}

		return createCA(p, d)
	}

	ca := &CA{
		Crt:      cd.Crt,
		db:       d,
		privKey:  cd.Key,
		passwd:   p.Passwd,
		serial:   cd.serial,
	}

	return ca, nil
}

// Export a PEM encoded CA certificate
func (ca *CA) PEM() []byte {
	crt := &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: ca.Crt.Raw,
	}

	return pem.EncodeToMemory(crt)
}

// Close the CA instance, flush the DB
func (ca *CA) Close() error {
	ca.db.close()
	ca.db = nil
	ca.Crt = nil
	ca.privKey = nil
	ca.serial = nil

	return nil
}

// Find a given cn and return the corresponding cert
func (ca *CA) Find(cn string) (*Cert, error) {
	if s, err := ca.db.getsrv(cn); err == nil {
		return &s.Cert, nil
	}

	if c, err := ca.db.getuser(cn); err == nil {
		return c, nil
	}

	return nil, ErrNotFound
}

// Find a server with a given common name
func (ca *CA) FindServer(cn string) (*Server, error) {
	return ca.db.getsrv(cn)
}

// Find a user with a given common name
func (ca *CA) FindUser(cn string) (*Cert, error) {
	return ca.db.getuser(cn)
}


// delete a user
func (ca *CA) DeleteUser(cn string) error {
	return ca.db.deluser(cn)
}

// Iterate over every cert in the list
func (ca *CA) MapServers(fp func(s *Server)) error {
	return ca.db.mapSrv(fp)
}

func (ca *CA) MapUsers(fp func(c *Cert)) error {
	return ca.db.mapUser(fp)
}

// Information needed in a certificate
type CertInfo struct {
	Subject  pkix.Name
	Validity time.Duration

	EmailAddresses []string
	DNSNames       []string

	// We only support exactly _one_ IP address
	IPAddress net.IP
	Port      uint16
}

// Generate and return a new server certificate
func (ca *CA) NewServerCert(ci *CertInfo) (*Cert, error) {
	if len(ci.IPAddress) == 0 && len(ci.DNSNames) == 0 {
		return nil, fmt.Errorf("Server IP/Hostname can't be empty")
	}

	if _, err := ca.db.getsrv(ci.Subject.CommonName); err == nil {
		return nil, ErrExists
	}

	// XXX Punt?
	if ci.Port == 0 {
		ci.Port = 1194
	}

	// We don't encrypt the server key; we need it in plain text
	// form when we export it..
	return ca.newCert(ci, true, "")
}

// Generate and return a new client certificate
func (ca *CA) NewClientCert(ci *CertInfo, pw string) (*Cert, error) {
	if _, err := ca.db.getuser(ci.Subject.CommonName); err == nil {
		return nil, ErrExists
	}

	return ca.newCert(ci, false, pw)
}

// Generate a new serial# for this CA instance
func (ca *CA) newSerial() *big.Int {
	n := big.NewInt(0).Add(ca.serial, big.NewInt(1))

	ca.serial = n
	return n
}

func newSerial() (*big.Int, error) {
	min := big.NewInt(1)
	min.Lsh(min, 127)

	max := big.NewInt(1)
	max.Lsh(max, 130)

	for {
		serial, err := rand.Int(rand.Reader, max)
		if err != nil {
			return nil, fmt.Errorf("ca: can't generate serial#: %s", err)
		}

		if serial.Cmp(min) > 0 {
			return serial, err
		}
	}
	panic("can't gen new CA serial")
}

// NewCA returns a DER encoded self-signed CA cert and CSR.
func createCA(p *CAparams, db *database) (*CA, error) {
	// Serial number
	serial, err := newSerial()
	if err != nil {
		return nil, err
	}

	// Generate a EC Private Key
	eckey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ca: can't generate ECC P256 key: %s", err)
	}

	pubkey := eckey.Public().(*ecdsa.PublicKey)
	akid := cksum(pubkey)

	now := time.Now().UTC()
	// Create the request template
	template := x509.Certificate{
		SignatureAlgorithm:    x509.ECDSAWithSHA512,
		PublicKeyAlgorithm:    x509.ECDSA,
		SerialNumber:          serial,
		Subject:               p.Subject,
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(p.Validity),
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,

		SubjectKeyId:   akid,
		AuthorityKeyId: akid,

		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}

	// self-sign the certificate authority
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, pubkey, eckey)
	if err != nil {
		return nil, fmt.Errorf("ca: can't create root cert: %s", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}

	z := &cadata{
		Cert: Cert{
			Crt: cert,
			Key: eckey,
		},
		serial: big.NewInt(0).Set(cert.SerialNumber),
	}

	err = db.putCA(z, p.Passwd)
	if err != nil {
		return nil, err
	}

	ca := &CA{
		Crt:      cert,
		db:       db,
		privKey:  eckey,
		passwd:   p.Passwd,
		serial:   z.serial,
	}

	return ca, nil
}

// generate and sign a new certificate for client or server (depending on isServer)
func (ca *CA) newCert(ci *CertInfo, isServer bool, pw string) (*Cert, error) {
	// Generate a EC Private Key
	eckey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("can't generate ECC P256 key: %s", err)
	}

	var val []byte
	var keyUsage x509.KeyUsage
	var extKeyUsage x509.ExtKeyUsage
	var tlscrypt []byte
	var ipaddrs []net.IP

	if isServer {
		// nsCert = Client
		val, err = asn1.Marshal(asn1.BitString{Bytes: []byte{0x40}, BitLength: 2})
		if err != nil {
			return nil, fmt.Errorf("can't marshal nsCertType: %s", err)
		}
		keyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement | x509.KeyUsageKeyEncipherment
		extKeyUsage = x509.ExtKeyUsageServerAuth

		tlscrypt = make([]byte, 256)
		_, err = io.ReadFull(rand.Reader, tlscrypt)
		if err != nil {
			panic("can't read tlscrypt random bytes")
		}

		if len(ci.IPAddress) > 0 {
			ipaddrs = []net.IP{ci.IPAddress}
		}
	} else {

		// nsCert = Client
		val, err = asn1.Marshal(asn1.BitString{Bytes: []byte{0x80}, BitLength: 2})
		if err != nil {
			return nil, fmt.Errorf("can't marshal nsCertType: %s", err)
		}
		keyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyAgreement
		extKeyUsage = x509.ExtKeyUsageClientAuth
	}

	pubkey := eckey.Public().(*ecdsa.PublicKey)
	skid := cksum(pubkey)
	now := time.Now().UTC()
	csr := &x509.Certificate{
		SignatureAlgorithm:    x509.ECDSAWithSHA512,
		PublicKeyAlgorithm:    x509.ECDSA,
		NotBefore:             now.Add(-1 * time.Minute),
		NotAfter:              now.Add(time.Duration(ci.Validity)),
		SerialNumber:          ca.newSerial(),
		Issuer:                ca.Crt.Subject,
		Subject:               ci.Subject,
		BasicConstraintsValid: true,

		SubjectKeyId: skid,

		DNSNames:       ci.DNSNames,
		IPAddresses:    ipaddrs,
		EmailAddresses: ci.EmailAddresses,

		KeyUsage:    keyUsage,
		ExtKeyUsage: []x509.ExtKeyUsage{extKeyUsage},
		ExtraExtensions: []pkix.Extension{
			{
				Id:    asn1.ObjectIdentifier{2, 16, 840, 1, 113730, 1, 1},
				Value: val,
			},
		},
	}

	// Sign with CA's private key
	cn := ci.Subject.CommonName
	der, err := x509.CreateCertificate(rand.Reader, csr, ca.Crt, pubkey, ca.privKey)
	if err != nil {
		return nil, fmt.Errorf("server cert '%s' can't be created: %s", cn, err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		panic(err)
	}

	crt := Cert{
		Crt: cert,
		Key: eckey,
	}

	if isServer {
		z := &Server{
			Cert: crt,
			ServerInfo: ServerInfo{
				IP:   ci.IPAddress,
				Port: ci.Port,
				TLS:  tlscrypt,
			},
		}

		err = ca.db.putsrv(z, "")
	} else {
		err = ca.db.putuser(&crt, pw)
	}

	if err != nil {
		return nil, err
	}

	return &crt, nil
}

var (
	ErrExists   = errors.New("common name exists in DB")
	ErrNotFound = errors.New("common name not found in DB")
	ErrTooSmall = errors.New("decrypt input buffer too small")
)
