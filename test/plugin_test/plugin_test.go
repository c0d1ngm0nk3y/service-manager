package plugin_test

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/Peripli/service-manager/test/common"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"

	"github.com/Peripli/service-manager/pkg/util"
	"github.com/Peripli/service-manager/pkg/web"
	"github.com/tidwall/sjson"
)

type object = common.Object

func TestPlugins(t *testing.T) {
	os.Chdir("../..")
	RegisterFailHandler(Fail)
	RunSpecs(t, "Plugin Tests Suite")
}

var _ = Describe("Service Manager Plugins", func() {
	var ctx *common.TestContext
	var testBroker *common.Broker

	AfterEach(func() {
		ctx.Cleanup()
	})

	Describe("Partial plugin", func() {
		BeforeEach(func() {
			ctx = common.NewTestContext(&common.ContextParams{
				RegisterExtensions: func(api *web.API) {
					api.RegisterPlugins(&PartialPlugin{})
				},
			})
			testBroker = ctx.RegisterBroker("broker1", nil)

		})

		It("should be called for provision and not for deprovision", func() {
			ctx.SMWithBasic.PUT(testBroker.OSBURL+"/v2/service_instances/1234").
				WithHeader("Content-Type", "application/json").
				WithJSON(object{}).
				Expect().Status(http.StatusOK).Header("X-Plugin").Equal("provision")

			ctx.SMWithBasic.DELETE(testBroker.OSBURL+"/v2/service_instances/1234").
				WithHeader("Content-Type", "application/json").
				WithJSON(object{}).
				Expect().Status(http.StatusOK).Header("X-Plugin").Empty()
		})
	})

	Describe("Complete plugin", func() {
		var testPlugin TestPlugin

		BeforeEach(func() {
			testPlugin = TestPlugin{}
		})

		JustBeforeEach(func() {
			ctx = common.NewTestContext(&common.ContextParams{
				RegisterExtensions: func(api *web.API) {
					api.RegisterPlugins(testPlugin)
				},
			})
			testBroker = ctx.RegisterBroker("broker1", nil)
		})

		It("Plugin modifies the request & response body", func() {
			var resBodySize int
			testPlugin["provision"] = web.MiddlewareFunc(func(req *web.Request, next web.Handler) (*web.Response, error) {
				var err error
				req.Body, err = sjson.SetBytes(req.Body, "extra", "request")
				if err != nil {
					return nil, err
				}

				res, err := next.Handle(req)
				if err != nil {
					return nil, err
				}

				res.Body, err = sjson.SetBytes(res.Body, "extra", "response")
				if err != nil {
					return nil, err
				}
				resBodySize = len(res.Body)
				return res, nil
			})
			testBroker.StatusCode = http.StatusCreated

			provisionBody := object{
				"service_id": "s123",
				"plan_id":    "p123",
			}
			resp := ctx.SMWithBasic.PUT(testBroker.OSBURL + "/v2/service_instances/1234").
				WithJSON(provisionBody).Expect().Status(http.StatusCreated)
			resp.Header("content-length").Equal(strconv.Itoa(resBodySize))
			reply := resp.JSON().Object()

			Expect(testBroker.Request.Header.Get("content-length")).To(Equal(
				strconv.Itoa(len(testBroker.RawRequestBody))))
			reply.ValueEqual("extra", "response")
			testBroker.RequestBody.Object().Equal(object{
				"service_id": "s123",
				"plan_id":    "p123",
				"extra":      "request",
			})
		})

		It("Plugin modifies the request & response headers", func() {
			testPlugin["fetchCatalog"] = web.MiddlewareFunc(func(req *web.Request, next web.Handler) (*web.Response, error) {
				h := req.Header.Get("extra")
				req.Header.Set("extra", h+"-request")

				res, err := next.Handle(req)
				if err != nil {
					return nil, err
				}

				res.Header.Set("extra", h+"-response")
				return res, nil
			})
			testBroker.StatusCode = http.StatusOK

			ctx.SMWithBasic.GET(testBroker.OSBURL+"/v2/catalog").WithHeader("extra", "value").
				Expect().Status(http.StatusOK).Header("extra").Equal("value-response")

			Expect(testBroker.Request.Header.Get("extra")).To(Equal("value-request"))
		})

		It("Plugin aborts the request", func() {
			testPlugin["fetchCatalog"] = web.MiddlewareFunc(func(req *web.Request, next web.Handler) (*web.Response, error) {
				return nil, &util.HTTPError{
					ErrorType:   "PluginErr",
					Description: "Plugin error",
					StatusCode:  http.StatusBadRequest,
				}
			})

			ctx.SMWithBasic.GET(testBroker.OSBURL + "/v2/catalog").
				Expect().Status(http.StatusBadRequest).JSON().Object().Equal(object{
				"error":       "PluginErr",
				"description": "Plugin error",
			})

			Expect(testBroker.Called()).To(BeFalse())
		})

		It("Request host header is properly set", func() {
			ctx.SMWithBasic.GET(testBroker.OSBURL + "/v2/catalog").
				Expect().Status(http.StatusOK)

			Expect(testBroker.Server.URL).To(ContainSubstring(testBroker.Request.Host))
		})

		osbOperations := []struct {
			name    string
			method  string
			path    string
			queries []string
		}{
			{"fetchCatalog", "GET", "/v2/catalog", []string{""}},
			{"provision", "PUT", "/v2/service_instances/1234", []string{"", "accepts_incomplete=true"}},
			{"deprovision", "DELETE", "/v2/service_instances/1234", []string{""}},
			{"updateService", "PATCH", "/v2/service_instances/1234", []string{""}},
			{"fetchService", "GET", "/v2/service_instances/1234", []string{""}},
			{"bind", "PUT", "/v2/service_instances/1234/service_bindings/111", []string{""}},
			{"unbind", "DELETE", "/v2/service_instances/1234/service_bindings/111", []string{""}},
			{"fetchBinding", "GET", "/v2/service_instances/1234/service_bindings/111", []string{""}},
			{"pollInstance", "GET", "/v2/service_instances/1234/last_operation", []string{"", "service_id=serviceId", "plan_id=planId", "operation=provision", "service_id=serviceId&plan_id=planId&operation=provision"}},
			{"pollBinding", "GET", "/v2/service_instances/1234/service_bindings/111/last_operation", []string{"", "service_id=serviceId", "plan_id=planId", "operation=provision", "service_id=serviceId&plan_id=planId&operation=provision"}},
		}

		for _, op := range osbOperations {
			op := op
			It(fmt.Sprintf("Plugin intercepts %s operation", op.name), func() {
				testPlugin[op.name] = web.MiddlewareFunc(func(req *web.Request, next web.Handler) (*web.Response, error) {
					res, err := next.Handle(req)
					if err == nil {
						res.Header.Set("X-Plugin", op.name)
					}
					return res, err
				})

				for _, query := range op.queries {
					ctx.SMWithBasic.Request(op.method, testBroker.OSBURL+op.path).
						WithHeader("Content-Type", "application/json").
						WithJSON(object{}).
						WithQueryString(query).
						Expect().Status(http.StatusOK).Header("X-Plugin").Equal(op.name)
				}
			})
		}

	})

})

type TestPlugin map[string]web.Middleware

func (p TestPlugin) Name() string { return "TestPlugin" }

func (p TestPlugin) call(middleware web.Middleware, req *web.Request, next web.Handler) (*web.Response, error) {
	if middleware == nil {
		return next.Handle(req)
	}
	return middleware.Run(req, next)
}

func (p TestPlugin) FetchCatalog(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["fetchCatalog"], request, next)
}

func (p TestPlugin) Provision(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["provision"], request, next)
}

func (p TestPlugin) Deprovision(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["deprovision"], request, next)
}

func (p TestPlugin) UpdateService(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["updateService"], request, next)
}
func (p TestPlugin) FetchService(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["fetchService"], request, next)
}

func (p TestPlugin) Bind(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["bind"], request, next)
}

func (p TestPlugin) Unbind(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["unbind"], request, next)
}

func (p TestPlugin) FetchBinding(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["fetchBinding"], request, next)
}

func (p TestPlugin) PollInstance(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["pollInstance"], request, next)
}

func (p TestPlugin) PollBinding(request *web.Request, next web.Handler) (*web.Response, error) {
	return p.call(p["pollBinding"], request, next)
}

type PartialPlugin struct{}

func (p PartialPlugin) Name() string { return "PartialPlugin" }

func (p PartialPlugin) Provision(request *web.Request, next web.Handler) (*web.Response, error) {
	res, err := next.Handle(request)
	if err == nil {
		res.Header.Set("X-Plugin", "provision")
	}
	return res, err
}
