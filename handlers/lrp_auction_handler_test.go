package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	fake_auction_runner "github.com/cloudfoundry-incubator/auction/auctiontypes/fakes"
	"github.com/cloudfoundry-incubator/auctioneer/handlers"
	"github.com/cloudfoundry-incubator/runtime-schema/models"
	fake_metrics_sender "github.com/cloudfoundry/dropsonde/metric_sender/fake"
	"github.com/cloudfoundry/dropsonde/metrics"
	"github.com/pivotal-golang/lager"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
)

var _ = Describe("LRPAuctionHandler", func() {
	var (
		logger           lager.Logger
		runner           *fake_auction_runner.FakeAuctionRunner
		responseRecorder *httptest.ResponseRecorder
		handler          *handlers.LRPAuctionHandler

		metricsSender *fake_metrics_sender.FakeMetricSender
	)

	BeforeEach(func() {
		logger = lager.NewLogger("test")
		logger.RegisterSink(lager.NewWriterSink(GinkgoWriter, lager.DEBUG))
		runner = new(fake_auction_runner.FakeAuctionRunner)
		responseRecorder = httptest.NewRecorder()
		handler = handlers.NewLRPAuctionHandler(runner, logger)

		metricsSender = fake_metrics_sender.NewFakeMetricSender()
		metrics.Initialize(metricsSender)
	})

	Describe("Create", func() {
		Context("when the request body is an LRP start auction request", func() {
			var start models.LRPStart

			BeforeEach(func() {
				start = models.LRPStart{}

				handler.Create(responseRecorder, newTestRequest(start))
			})

			It("responds with 201", func() {
				Ω(responseRecorder.Code).Should(Equal(http.StatusCreated))
			})

			It("responds with an empty JSON body", func() {
				Ω(responseRecorder.Body.String()).Should(Equal("{}"))
			})

			It("should submit the start auction to the auction runner", func() {
				Ω(runner.AddLRPStartForAuctionCallCount()).Should(Equal(1))

				submittedStart := runner.AddLRPStartForAuctionArgsForCall(0)
				Ω(submittedStart).Should(Equal(start))
			})

			It("should increment the start auction auction started metric", func() {
				Eventually(func() uint64 {
					return metricsSender.GetCounter("AuctioneerStartAuctionsStarted")
				}).Should(Equal(uint64(1)))
			})
		})

		Context("when the request body is a not a start auction", func() {
			BeforeEach(func() {
				handler.Create(responseRecorder, newTestRequest(`{invalidjson}`))
			})

			It("responds with 400", func() {
				Ω(responseRecorder.Code).Should(Equal(http.StatusBadRequest))
			})

			It("responds with a JSON body containing the error", func() {
				handlerError := handlers.HandlerError{}
				err := json.NewDecoder(responseRecorder.Body).Decode(&handlerError)
				Ω(err).ShouldNot(HaveOccurred())
				Ω(handlerError.Error).ShouldNot(BeEmpty())
			})

			It("should not submit the start auction to the auction runner", func() {
				Ω(runner.AddLRPStartForAuctionCallCount()).Should(Equal(0))
			})

			It("should not increment the start auction auction started metric", func() {
				Consistently(func() uint64 {
					return metricsSender.GetCounter("AuctioneerStartAuctionsStarted")
				}).Should(Equal(uint64(0)))
			})
		})
	})
})