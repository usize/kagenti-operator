/*
Copyright 2025.

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

package v1alpha1

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	kagentioperatordevv1alpha1 "github.com/kagenti/operator/platform/api/v1alpha1"
	// TODO (user): Add any additional imports if needed
)

var _ = Describe("Component Webhook", func() {
	var (
		obj       *kagentioperatordevv1alpha1.Component
		oldObj    *kagentioperatordevv1alpha1.Component
		validator ComponentCustomValidator
		defaulter ComponentCustomDefaulter
	)

	BeforeEach(func() {
		obj = &kagentioperatordevv1alpha1.Component{}
		oldObj = &kagentioperatordevv1alpha1.Component{}
		validator = ComponentCustomValidator{}
		Expect(validator).NotTo(BeNil(), "Expected validator to be initialized")
		defaulter = ComponentCustomDefaulter{}
		Expect(defaulter).NotTo(BeNil(), "Expected defaulter to be initialized")
		Expect(oldObj).NotTo(BeNil(), "Expected oldObj to be initialized")
		Expect(obj).NotTo(BeNil(), "Expected obj to be initialized")
		// TODO (user): Add any setup logic common to all tests
	})

	AfterEach(func() {
		// TODO (user): Add any teardown logic common to all tests
	})

	Context("When creating Component under Defaulting Webhook", func() {
		// TODO (user): Add logic for defaulting webhooks
		// Example:
		// It("Should apply defaults when a required field is empty", func() {
		//     By("simulating a scenario where defaults should be applied")
		//     obj.SomeFieldWithDefault = ""
		//     By("calling the Default method to apply defaults")
		//     defaulter.Default(ctx, obj)
		//     By("checking that the default values are set")
		//     Expect(obj.SomeFieldWithDefault).To(Equal("default_value"))
		// })
	})

	Context("When creating or updating Component under Validating Webhook", func() {
		// TODO (user): Add logic for validating webhooks
		// Example:
		// It("Should deny creation if a required field is missing", func() {
		//     By("simulating an invalid creation scenario")
		//     obj.SomeRequiredField = ""
		//     Expect(validator.ValidateCreate(ctx, obj)).Error().To(HaveOccurred())
		// })
		//
		// It("Should admit creation if all required fields are present", func() {
		//     By("simulating an invalid creation scenario")
		//     obj.SomeRequiredField = "valid_value"
		//     Expect(validator.ValidateCreate(ctx, obj)).To(BeNil())
		// })
		//
		// It("Should validate updates correctly", func() {
		//     By("simulating a valid update scenario")
		//     oldObj.SomeRequiredField = "updated_value"
		//     obj.SomeRequiredField = "updated_value"
		//     Expect(validator.ValidateUpdate(ctx, oldObj, obj)).To(BeNil())
		// })

		It("Should not panic when validating Component with agent.build but no deployer.kubernetes", func() {
			By("creating a Component with only agent.build configuration")
			obj.Spec.Agent = &kagentioperatordevv1alpha1.AgentComponent{
				Build: &kagentioperatordevv1alpha1.BuildSpec{
					SourceRepository: "https://github.com/example/repo",
					SourceRevision:   "main",
					SourceSubfolder:  "agent",
				},
			}

			By("ensuring deployer.kubernetes is nil")
			obj.Spec.Deployer.Kubernetes = nil

			By("calling validateComponentSpec should not panic")
			errors := validator.validateComponentSpec(obj)

			By("checking that validation runs without crashing")
			Expect(errors).NotTo(BeNil())
		})
	})

})
