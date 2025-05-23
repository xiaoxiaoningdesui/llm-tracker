/*
The MIT License (MIT)

Copyright (c) 2016-2020 Containous SAS; 2020-2025 Traefik Labs

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in
all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN
THE SOFTWARE.
*/

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	"net/http"

	"github.com/traefik/traefik/v2/pkg/provider/kubernetes/crd/generated/clientset/versioned/scheme"
	v1alpha1 "github.com/traefik/traefik/v2/pkg/provider/kubernetes/crd/traefikcontainous/v1alpha1"
	rest "k8s.io/client-go/rest"
)

type TraefikContainousV1alpha1Interface interface {
	RESTClient() rest.Interface
	IngressRoutesGetter
	IngressRouteTCPsGetter
	IngressRouteUDPsGetter
	MiddlewaresGetter
	MiddlewareTCPsGetter
	ServersTransportsGetter
	TLSOptionsGetter
	TLSStoresGetter
	TraefikServicesGetter
}

// TraefikContainousV1alpha1Client is used to interact with features provided by the traefik.containo.us group.
type TraefikContainousV1alpha1Client struct {
	restClient rest.Interface
}

func (c *TraefikContainousV1alpha1Client) IngressRoutes(namespace string) IngressRouteInterface {
	return newIngressRoutes(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) IngressRouteTCPs(namespace string) IngressRouteTCPInterface {
	return newIngressRouteTCPs(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) IngressRouteUDPs(namespace string) IngressRouteUDPInterface {
	return newIngressRouteUDPs(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) Middlewares(namespace string) MiddlewareInterface {
	return newMiddlewares(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) MiddlewareTCPs(namespace string) MiddlewareTCPInterface {
	return newMiddlewareTCPs(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) ServersTransports(namespace string) ServersTransportInterface {
	return newServersTransports(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) TLSOptions(namespace string) TLSOptionInterface {
	return newTLSOptions(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) TLSStores(namespace string) TLSStoreInterface {
	return newTLSStores(c, namespace)
}

func (c *TraefikContainousV1alpha1Client) TraefikServices(namespace string) TraefikServiceInterface {
	return newTraefikServices(c, namespace)
}

// NewForConfig creates a new TraefikContainousV1alpha1Client for the given config.
// NewForConfig is equivalent to NewForConfigAndClient(c, httpClient),
// where httpClient was generated with rest.HTTPClientFor(c).
func NewForConfig(c *rest.Config) (*TraefikContainousV1alpha1Client, error) {
	config := *c
	if err := setConfigDefaults(&config); err != nil {
		return nil, err
	}
	httpClient, err := rest.HTTPClientFor(&config)
	if err != nil {
		return nil, err
	}
	return NewForConfigAndClient(&config, httpClient)
}

// NewForConfigAndClient creates a new TraefikContainousV1alpha1Client for the given config and http client.
// Note the http client provided takes precedence over the configured transport values.
func NewForConfigAndClient(c *rest.Config, h *http.Client) (*TraefikContainousV1alpha1Client, error) {
	config := *c
	if err := setConfigDefaults(&config); err != nil {
		return nil, err
	}
	client, err := rest.RESTClientForConfigAndClient(&config, h)
	if err != nil {
		return nil, err
	}
	return &TraefikContainousV1alpha1Client{client}, nil
}

// NewForConfigOrDie creates a new TraefikContainousV1alpha1Client for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *TraefikContainousV1alpha1Client {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

// New creates a new TraefikContainousV1alpha1Client for the given RESTClient.
func New(c rest.Interface) *TraefikContainousV1alpha1Client {
	return &TraefikContainousV1alpha1Client{c}
}

func setConfigDefaults(config *rest.Config) error {
	gv := v1alpha1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}

	return nil
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *TraefikContainousV1alpha1Client) RESTClient() rest.Interface {
	if c == nil {
		return nil
	}
	return c.restClient
}
