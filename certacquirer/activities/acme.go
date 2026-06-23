// -------------------------------------------------------------------------------
// Cert Acquirer - lego/ACME Adapter
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// The thin adapter that satisfies the certIssuer consumer interface (declared in
// activities.go) with a real go-acme/lego client and the Cloudflare DNS-01
// provider. This is the live ACME + Cloudflare I/O -- it can't be unit-tested
// without a real ACME server and DNS, so this file is excluded from the coverage
// metric (sonar-project.properties). The issuance *orchestration* that drives
// this interface lives in activities.go and is fully covered against a fake.
// -------------------------------------------------------------------------------

package activities

import (
	"context"
	"fmt"

	"github.com/go-acme/lego/v4/certcrypto"
	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/cloudflare"
	"github.com/go-acme/lego/v4/registration"
)

// legoIssuer adapts a configured lego client to the certIssuer interface.
type legoIssuer struct {
	client *lego.Client
}

// Register registers a new ACME account with the CA.
func (l *legoIssuer) Register(_ context.Context) (*registration.Resource, error) {
	return l.client.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
}

// Obtain runs the DNS-01 challenge for domains and returns the issued cert+key
// as PEM bytes.
func (l *legoIssuer) Obtain(_ context.Context, domains []string) (certPEM, keyPEM []byte, err error) {
	res, err := l.client.Certificate.Obtain(certificate.ObtainRequest{Domains: domains, Bundle: true})
	if err != nil {
		return nil, nil, err
	}
	return res.Certificate, res.PrivateKey, nil
}

// newLegoIssuer builds a lego client for user with the Cloudflare DNS-01 provider
// wired from the token in Vault. The leaf key is RSA2048 while the account key is
// EC256 -- they are independent by design. This is the default Activities.newIssuer
// set in New; tests substitute a fake.
func (a *Activities) newLegoIssuer(ctx context.Context, user *acmeUser) (certIssuer, error) {
	config := lego.NewConfig(user)
	config.CADirURL = a.cfg.CADirURL
	config.Certificate.KeyType = certcrypto.RSA2048

	client, err := lego.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create acme client: %w", err)
	}

	cfToken, err := a.cfg.Vault.ReadKVField(ctx, a.cfg.CFTokenPath, a.cfg.CFTokenField)
	if err != nil {
		return nil, fmt.Errorf("read cloudflare token: %w", err)
	}
	cfCfg := cloudflare.NewDefaultConfig()
	cfCfg.AuthToken = cfToken
	provider, err := cloudflare.NewDNSProviderConfig(cfCfg)
	if err != nil {
		return nil, fmt.Errorf("create cloudflare dns provider: %w", err)
	}
	if err := client.Challenge.SetDNS01Provider(provider); err != nil {
		return nil, fmt.Errorf("set dns-01 provider: %w", err)
	}
	return &legoIssuer{client: client}, nil
}
