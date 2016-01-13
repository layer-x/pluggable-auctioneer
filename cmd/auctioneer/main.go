package main

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/nu7hatch/gouuid"

	"github.com/cloudfoundry-incubator/auctioneer"
	"github.com/cloudfoundry-incubator/auctioneer/auctionmetricemitterdelegate"
	"github.com/cloudfoundry-incubator/auctioneer/auctionrunnerdelegate"
	"github.com/cloudfoundry-incubator/auctioneer/handlers"
	"github.com/cloudfoundry-incubator/bbs"
	"github.com/cloudfoundry-incubator/cf-debug-server"
	cf_lager "github.com/cloudfoundry-incubator/cf-lager"
	"github.com/cloudfoundry-incubator/cf_http"
	"github.com/cloudfoundry-incubator/consuladapter"
	"github.com/cloudfoundry-incubator/locket"
	"github.com/cloudfoundry-incubator/rep"
	"github.com/pivotal-golang/lager"
	"github.com/pivotal-golang/localip"

	"github.com/cloudfoundry-incubator/auction/auctionrunner"
	"github.com/cloudfoundry-incubator/auction/auctiontypes"
	"github.com/cloudfoundry/dropsonde"
	"github.com/cloudfoundry/gunk/workpool"
	"github.com/pivotal-golang/clock"
	"github.com/tedsuo/ifrit"
	"github.com/tedsuo/ifrit/grouper"
	"github.com/tedsuo/ifrit/http_server"
	"github.com/tedsuo/ifrit/sigmon"
"net/http"
"io/ioutil"
"encoding/json"
)

var communicationTimeout = flag.Duration(
	"communicationTimeout",
	10*time.Second,
	"Timeout applied to all HTTP requests.",
)

var cellStateTimeout = flag.Duration(
	"cellStateTimeout",
	1*time.Second,
	"Timeout applied to HTTP requests to the Cell State endpoint.",
)

var consulCluster = flag.String(
	"consulCluster",
	"",
	"comma-separated list of consul server addresses (ip:port)",
)

var dropsondePort = flag.Int(
	"dropsondePort",
	3457,
	"port the local metron agent is listening on",
)

var lockTTL = flag.Duration(
	"lockTTL",
	locket.LockTTL,
	"TTL for service lock",
)

var lockRetryInterval = flag.Duration(
	"lockRetryInterval",
	locket.RetryInterval,
	"interval to wait before retrying a failed lock acquisition",
)

var listenAddr = flag.String(
	"listenAddr",
	"0.0.0.0:9016",
	"host:port to serve auction and LRP stop requests on",
)

var bbsAddress = flag.String(
	"bbsAddress",
	"",
	"Address to the BBS Server",
)

var bbsCACert = flag.String(
	"bbsCACert",
	"",
	"path to certificate authority cert used for mutually authenticated TLS BBS communication",
)

var bbsClientCert = flag.String(
	"bbsClientCert",
	"",
	"path to client cert used for mutually authenticated TLS BBS communication",
)

var bbsClientKey = flag.String(
	"bbsClientKey",
	"",
	"path to client key used for mutually authenticated TLS BBS communication",
)

var bbsClientSessionCacheSize = flag.Int(
	"bbsClientSessionCacheSize",
	0,
	"Capacity of the ClientSessionCache option on the TLS configuration. If zero, golang's default will be used",
)

var bbsMaxIdleConnsPerHost = flag.Int(
	"bbsMaxIdleConnsPerHost",
	0,
	"Controls the maximum number of idle (keep-alive) connctions per host. If zero, golang's default will be used",
)

var auctionRunnerWorkers = flag.Int(
	"auctionRunnerWorkers",
	1000,
	"Max concurrency for cell operations in the auction runner",
)

const (
	auctionRunnerTimeout = 10 * time.Second
	dropsondeOrigin      = "auctioneer"
	serverProtocol       = "http"
)

func main() {
	cf_debug_server.AddFlags(flag.CommandLine)
	cf_lager.AddFlags(flag.CommandLine)
	flag.Parse()

	cf_http.Initialize(*communicationTimeout)

	logger, reconfigurableSink := cf_lager.New("auctioneer")
	initializeDropsonde(logger)

	if err := validateBBSAddress(); err != nil {
		logger.Fatal("invalid-bbs-address", err)
	}

	client, err := consuladapter.NewClient(*consulCluster)
	if err != nil {
		logger.Fatal("new-client-failed", err)
	}

	sessionMgr := consuladapter.NewSessionManager(client)
	consulSession, err := consuladapter.NewSession("auctioneer", *lockTTL, client, sessionMgr)
	if err != nil {
		logger.Fatal("consul-session-failed", err)
	}

	clock := clock.NewClock()
	bbsServiceClient := bbs.NewServiceClient(consulSession, clock)
	auctioneerServiceClient := auctioneer.NewServiceClient(consulSession, clock)

	auctionRunner := initializeAuctionRunner(logger, *cellStateTimeout, initializeBBSClient(logger), bbsServiceClient)
	auctionServer := initializeAuctionServer(logger, auctionRunner)
	lockMaintainer := initializeLockMaintainer(logger, auctioneerServiceClient)

	members := grouper.Members{
		{"lock-maintainer", lockMaintainer},
		{"auction-runner", auctionRunner},
		{"auction-server", auctionServer},
	}

	if dbgAddr := cf_debug_server.DebugAddress(flag.CommandLine); dbgAddr != "" {
		members = append(grouper.Members{
			{"debug-server", cf_debug_server.Runner(dbgAddr, reconfigurableSink)},
		}, members...)
	}

	group := grouper.NewOrdered(os.Interrupt, members)

	monitor := ifrit.Invoke(sigmon.New(group))

	logger.Info("started")

	err = <-monitor.Wait()
	if err != nil {
		logger.Error("exited-with-failure", err)
		os.Exit(1)
	}

	logger.Info("exited")
}

func initializeAuctionRunner(logger lager.Logger, cellStateTimeout time.Duration, bbsClient bbs.Client, serviceClient bbs.ServiceClient) auctiontypes.AuctionRunner {
	httpClient := cf_http.NewClient()
	stateClient := cf_http.NewCustomTimeoutClient(cellStateTimeout)
	repClientFactory := rep.NewClientFactory(httpClient, stateClient)

	delegate := auctionrunnerdelegate.New(repClientFactory, bbsClient, serviceClient, logger)
	metricEmitter := auctionmetricemitterdelegate.New()
	workPool, err := workpool.NewWorkPool(*auctionRunnerWorkers)
	if err != nil {
		logger.Fatal("failed-to-construct-auction-runner-workpool", err, lager.Data{"num-workers": *auctionRunnerWorkers}) // should never happen
	}

	brains := make(map[string]auctionrunner.Brain)
	var defaultBrain auctionrunner.Brain
	var ListenForBrain = func() {
		fmt.Printf("\n\n\nILACKARMS\n\n\n")
		defaultBrainChan := make(chan auctionrunner.Brain)
		m := http.NewServeMux()
		m.HandleFunc("/Start", func(w http.ResponseWriter, req *http.Request) {
			data, err := ioutil.ReadAll(req.Body)
			if req.Body != nil {
				defer req.Body.Close()
			}
			if err != nil {
				fmt.Printf("\n\nSomething really bad happened! Couldnt read NEW BRAIN request: %v\n\n", err)
				os.Exit(-1)
			}
			var newBrainData struct{
				Name string `json:"name"`
				Url string `json:"url"`
				Tags string `json:"tags"`
			}
			err = json.Unmarshal(data, &newBrainData)
			if err != nil {
				fmt.Printf("\n\nSomething really bad happened! Couldnt read unmarshall %s to NEW BRAIN: %v\n\n", string(data), err)
				os.Exit(-1)
			}
			newBrain := auctionrunner.NewBrain(newBrainData.Name, newBrainData.Url, newBrainData.Tags)
			if strings.Contains(newBrainData.Tags, "default") {
				if _, ok := brains["default"] ; !ok {
					defaultBrainChan <- newBrain
					fmt.Printf("\nLETS A GO\n")
				}
			}
			tags := strings.Split(newBrainData.Tags, ",")
			for _, tag := range tags {
				fmt.Printf("\nAdding Brain: %s to task-type %s\n", newBrain.Name, tag)
				brains[tag] = newBrain
			}
		})
		go http.ListenAndServe(":3000", m)
		defaultBrain = <-defaultBrainChan
		brains["default"] = defaultBrain
	}

	ListenForBrain()

	return auctionrunner.New(
		delegate,
		metricEmitter,
		clock.NewClock(),
		workPool,
		logger,
		brains,
	)
}

func initializeDropsonde(logger lager.Logger) {
	dropsondeDestination := fmt.Sprint("localhost:", *dropsondePort)
	err := dropsonde.Initialize(dropsondeDestination, dropsondeOrigin)
	if err != nil {
		logger.Error("failed to initialize dropsonde: %v", err)
	}
}

func initializeAuctionServer(logger lager.Logger, runner auctiontypes.AuctionRunner) ifrit.Runner {
	return http_server.New(*listenAddr, handlers.New(runner, logger))
}

func initializeLockMaintainer(logger lager.Logger, serviceClient auctioneer.ServiceClient) ifrit.Runner {
	uuid, err := uuid.NewV4()
	if err != nil {
		logger.Fatal("Couldn't generate uuid", err)
	}

	localIP, err := localip.LocalIP()
	if err != nil {
		logger.Fatal("Couldn't determine local IP", err)
	}

	port := strings.Split(*listenAddr, ":")[1]
	address := fmt.Sprintf("%s://%s:%s", serverProtocol, localIP, port)
	auctioneerPresence := auctioneer.NewPresence(uuid.String(), address)

	lockMaintainer, err := serviceClient.NewAuctioneerLockRunner(logger, auctioneerPresence, *lockRetryInterval)
	if err != nil {
		logger.Fatal("Couldn't create lock maintainer", err)
	}

	return lockMaintainer
}

func validateBBSAddress() error {
	if *bbsAddress == "" {
		return errors.New("bbsAddress is required")
	}
	return nil
}

func initializeBBSClient(logger lager.Logger) bbs.Client {
	bbsURL, err := url.Parse(*bbsAddress)
	if err != nil {
		logger.Fatal("Invalid BBS URL", err)
	}

	if bbsURL.Scheme != "https" {
		return bbs.NewClient(*bbsAddress)
	}

	bbsClient, err := bbs.NewSecureClient(*bbsAddress, *bbsCACert, *bbsClientCert, *bbsClientKey, *bbsClientSessionCacheSize, *bbsMaxIdleConnsPerHost)
	if err != nil {
		logger.Fatal("Failed to configure secure BBS client", err)
	}
	return bbsClient
}
