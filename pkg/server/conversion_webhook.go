/*
Copyright 2025 The KCP Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package server

import (
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/apiserver/conversion"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apiserver/pkg/util/webhook"

	"github.com/kcp-dev/kcp/sdk/apis/apis"
	apisv1alpha1 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha1"
	apisv1alpha2 "github.com/kcp-dev/kcp/sdk/apis/apis/v1alpha2"
)

var _ conversion.Factory = &CRConverterFactory{}

type CRConverterFactory struct {
	delegate *conversion.CRConverterFactory
	scheme   *runtime.Scheme
}

// NewCRConverterFactory returns a wrapper around a conversion.CRConverterFactory that intercepts
// conversion requests for apis.kcp.io and calls the conversion functions directly. All other
// API groups are processed using the delegated converter.
func NewCRConverterFactory(serviceResolver webhook.ServiceResolver, authResolverWrapper webhook.AuthenticationInfoResolverWrapper) (*CRConverterFactory, error) {
	delegate, err := conversion.NewCRConverterFactory(serviceResolver, authResolverWrapper)
	if err != nil {
		return nil, err
	}

	scheme := runtime.NewScheme()
	if err := apisv1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := apisv1alpha2.AddToScheme(scheme); err != nil {
		return nil, err
	}

	return &CRConverterFactory{
		delegate: delegate,
		scheme:   scheme,
	}, nil
}

func (f *CRConverterFactory) NewConverter(crd *apiextensionsv1.CustomResourceDefinition) (runtime.ObjectConvertor, runtime.ObjectConvertor, error) {
	if crd.Spec.Group == apis.GroupName {
		schemaConverter := conversion.NewCRConverter(&schemaBasedConverter{
			crd:    crd,
			scheme: f.scheme,
		})
		return conversion.NewSafeConverterWrapper(schemaConverter), schemaConverter, nil
	}

	return f.delegate.NewConverter(crd)
}

// var _ conversion.CRConverterInterface = &schemaBasedConverter{}

type schemaBasedConverter struct {
	crd    *apiextensionsv1.CustomResourceDefinition
	scheme *runtime.Scheme
}

func (s *schemaBasedConverter) Convert(in runtime.Object, targetGV schema.GroupVersion) (runtime.Object, error) {
	list, isList := in.(*unstructured.UnstructuredList)
	if !isList {
		return nil, fmt.Errorf("expected unstructured.UnstructuredList, got %T", in)
	}

	out := &unstructured.UnstructuredList{}
	for _, item := range list.Items {
		obj, err := s.scheme.New(item.GroupVersionKind())
		if err != nil {
			return nil, err
		}

		err = runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, obj)
		if err != nil {
			return nil, err
		}

		converted, err := s.scheme.ConvertToVersion(obj, targetGV)
		if err != nil {
			return nil, err
		}

		uns, err := runtime.DefaultUnstructuredConverter.ToUnstructured(converted)
		if err != nil {
			return nil, err
		}

		newObj := unstructured.Unstructured{}
		newObj.Object = uns

		out.Items = append(out.Items, newObj)
	}

	return out, nil
}
