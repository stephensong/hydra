package handler

import (
	log "github.com/ory-am/hydra/Godeps/_workspace/src/github.com/Sirupsen/logrus"
	"github.com/ory-am/hydra/Godeps/_workspace/src/github.com/codegangsta/cli"
	"github.com/ory-am/hydra/Godeps/_workspace/src/github.com/gorilla/mux"
	"github.com/ory-am/hydra/Godeps/_workspace/src/github.com/ory-am/ladon/guard"
	accounts "github.com/ory-am/hydra/account/handler"
	"github.com/ory-am/hydra/jwt"
	"github.com/ory-am/hydra/middleware/host"
	middleware "github.com/ory-am/hydra/middleware/host"
	clients "github.com/ory-am/hydra/oauth/client/handler"
	connections "github.com/ory-am/hydra/oauth/connection/handler"
	oauth "github.com/ory-am/hydra/oauth/handler"
	"github.com/ory-am/hydra/oauth/provider"
	policies "github.com/ory-am/hydra/policy/handler"

	"fmt"
	"net/http"
	"strconv"

	"github.com/ory-am/hydra/Godeps/_workspace/src/github.com/RangelReale/osin"
	"github.com/ory-am/hydra/Godeps/_workspace/src/github.com/ory-am/common/pkg"
	"github.com/ory-am/hydra/Godeps/_workspace/src/golang.org/x/net/http2"
)

type Core struct {
	Ctx               Context
	accountHandler    *accounts.Handler
	clientHandler     *clients.Handler
	connectionHandler *connections.Handler
	oauthHandler      *oauth.Handler
	policyHandler     *policies.Handler

	guard     guard.Guarder
	providers provider.Registry

	issuer   string
	audience string
}

func osinConfig() (conf *osin.ServerConfig, err error) {
	conf = osin.NewServerConfig()
	lifetime, err := strconv.Atoi(accessTokenLifetime)
	if err != nil {
		return nil, err
	}
	conf.AccessExpiration = int32(lifetime)

	conf.AllowedAuthorizeTypes = osin.AllowedAuthorizeType{
		osin.CODE,
		osin.TOKEN,
	}
	conf.AllowedAccessTypes = osin.AllowedAccessType{
		osin.AUTHORIZATION_CODE,
		osin.REFRESH_TOKEN,
		osin.PASSWORD,
		osin.CLIENT_CREDENTIALS,
	}
	conf.AllowGetAccessRequest = false
	conf.AllowClientSecretInParams = false
	conf.ErrorStatusCode = http.StatusInternalServerError
	conf.RedirectUriSeparator = "|"
	return conf, nil
}

func (c *Core) Start(ctx *cli.Context) error {
	// Start the database backend
	if err := c.Ctx.Start(); err != nil {
		return fmt.Errorf("Could not start context: %s", err)
	}

	private, err := jwt.LoadCertificate(jwtPrivateKeyPath)
	if err != nil {
		return fmt.Errorf("Could not load private key: %s", err)
	}

	public, err := jwt.LoadCertificate(jwtPublicKeyPath)
	if err != nil {
		return fmt.Errorf("Could not load public key: %s", err)
	}

	osinConf, err := osinConfig()
	if err != nil {
		return fmt.Errorf("Could not configure server: %s", err)
	}

	j := jwt.New(private, public)
	m := middleware.New(c.Ctx.GetPolicies(), j)
	c.guard = new(guard.Guard)
	c.accountHandler = accounts.NewHandler(c.Ctx.GetAccounts(), m)
	c.clientHandler = clients.NewHandler(c.Ctx.GetOsins(), m)
	c.connectionHandler = connections.NewHandler(c.Ctx.GetConnections(), m)
	c.providers = provider.NewRegistry(providers)
	c.policyHandler = policies.NewHandler(c.Ctx.GetPolicies(), m, c.guard, j, c.Ctx.GetOsins())
	c.oauthHandler = &oauth.Handler{
		Accounts:       c.Ctx.GetAccounts(),
		Policies:       c.Ctx.GetPolicies(),
		Guard:          c.guard,
		Connections:    c.Ctx.GetConnections(),
		Providers:      c.providers,
		Issuer:         c.issuer,
		Audience:       c.audience,
		JWT:            j,
		OAuthConfig:    osinConf,
		OAuthStore:     c.Ctx.GetOsins(),
		States:         c.Ctx.GetStates(),
		SignUpLocation: locations["signUp"],
		SignInLocation: locations["signIn"],
		Middleware:     host.New(c.Ctx.GetPolicies(), j),
	}

	extractor := m.ExtractAuthentication
	router := mux.NewRouter()
	c.accountHandler.SetRoutes(router, extractor)
	c.connectionHandler.SetRoutes(router, extractor)
	c.clientHandler.SetRoutes(router, extractor)
	c.oauthHandler.SetRoutes(router, extractor)
	c.policyHandler.SetRoutes(router, extractor)

	// TODO un-hack this, add database check, add error response
	router.HandleFunc("/alive", func(w http.ResponseWriter, r *http.Request) {
		pkg.WriteJSON(w, &struct {
			Status string `json:"status"`
		}{
			Status: "alive",
		})
	})

	log.Infoln("Hydra started")

	if forceHTTP == "force" {
		http.Handle("/", router)
		log.Warn("You're using HTTP without TLS encryption. This is dangerously unsafe and you should not do this.")
		if err := http.ListenAndServe(listenOn, nil); err != nil {
			return fmt.Errorf("Could not serve HTTP server because %s", err)
		}
		return nil
	}

	http.Handle("/", router)
	srv := &http.Server{Addr: listenOn}
	http2.ConfigureServer(srv, &http2.Server{})
	if err := srv.ListenAndServeTLS(tlsCertPath, tlsKeyPath); err != nil {
		return fmt.Errorf("Could not serve HTTP/2 server because %s", err)
	}

	return nil
}
