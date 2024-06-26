/*
Copyright 2018 Pusher Ltd. and Wave Contributors

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

package daemonset

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/wave-k8s/wave/pkg/core"
	"github.com/wave-k8s/wave/test/utils"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	webhook "sigs.k8s.io/controller-runtime/pkg/webhook"
)

var _ = Describe("DaemonSet controller Suite", func() {
	var c client.Client
	var m utils.Matcher

	var daemonset *appsv1.DaemonSet
	var requestsStart <-chan reconcile.Request
	var requests <-chan reconcile.Request

	const timeout = time.Second * 5
	const consistentlyTimeout = time.Second

	var cm1 *corev1.ConfigMap
	var cm2 *corev1.ConfigMap
	var cm3 *corev1.ConfigMap
	var s1 *corev1.Secret
	var s2 *corev1.Secret
	var s3 *corev1.Secret

	const modified = "modified"

	var waitForDaemonSetReconciled = func(obj core.Object) {
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
			},
		}
		// wait for reconcile for creating the DaemonSet
		Eventually(requestsStart, timeout).Should(Receive(Equal(request)))
		Eventually(requests, timeout).Should(Receive(Equal(request)))
	}

	var consistentlyDaemonSetNotReconciled = func(obj core.Object) {
		request := reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      obj.GetName(),
				Namespace: obj.GetNamespace(),
			},
		}
		// wait for reconcile for creating the DaemonSet
		Consistently(requestsStart, consistentlyTimeout).ShouldNot(Receive(Equal(request)))
	}

	var clearReconciled = func() {
		for len(requestsStart) > 0 {
			<-requestsStart
			<-requests
		}
	}

	BeforeEach(func() {
		// Reset the Prometheus Registry before each test to avoid errors
		metrics.Registry = prometheus.NewRegistry()

		mgr, err := manager.New(cfg, manager.Options{
			Metrics: metricsserver.Options{
				BindAddress: "0",
			},
			WebhookServer: webhook.NewServer(webhook.Options{
				Host:    t.WebhookInstallOptions.LocalServingHost,
				Port:    t.WebhookInstallOptions.LocalServingPort,
				CertDir: t.WebhookInstallOptions.LocalServingCertDir,
			}),
			Cache: cache.Options{
				DefaultNamespaces: core.BuildCacheDefaultNamespaces(""),
			},
		})
		Expect(err).NotTo(HaveOccurred())
		var cerr error
		c, cerr = client.New(cfg, client.Options{Scheme: scheme.Scheme})
		Expect(cerr).NotTo(HaveOccurred())
		m = utils.Matcher{Client: c}

		var recFn reconcile.Reconciler
		r := newReconciler(mgr)
		recFn, requestsStart, requests = SetupTestReconcile(r)
		Expect(add(mgr, recFn, r.handler)).NotTo(HaveOccurred())

		// register mutating pod webhook
		err = AddDaemonSetWebhook(mgr)
		Expect(err).ToNot(HaveOccurred())

		testCtx, testCancel = context.WithCancel(context.Background())
		go Run(testCtx, mgr)

		// Create some configmaps and secrets
		cm1 = utils.ExampleConfigMap1.DeepCopy()
		cm2 = utils.ExampleConfigMap2.DeepCopy()
		cm3 = utils.ExampleConfigMap3.DeepCopy()
		s1 = utils.ExampleSecret1.DeepCopy()
		s2 = utils.ExampleSecret2.DeepCopy()
		s3 = utils.ExampleSecret3.DeepCopy()

		m.Create(cm1).Should(Succeed())
		m.Create(cm2).Should(Succeed())
		m.Create(cm3).Should(Succeed())
		m.Create(s1).Should(Succeed())
		m.Create(s2).Should(Succeed())
		m.Create(s3).Should(Succeed())
		m.Get(cm1, timeout).Should(Succeed())
		m.Get(cm2, timeout).Should(Succeed())
		m.Get(cm3, timeout).Should(Succeed())
		m.Get(s1, timeout).Should(Succeed())
		m.Get(s2, timeout).Should(Succeed())
		m.Get(s3, timeout).Should(Succeed())

		daemonset = utils.ExampleDaemonSet.DeepCopy()
	})

	AfterEach(func() {
		testCancel()

		utils.DeleteAll(cfg, timeout,
			&appsv1.DaemonSetList{},
			&corev1.ConfigMapList{},
			&corev1.SecretList{},
			&corev1.EventList{},
		)
	})

	Context("When a DaemonSet with all children existing is reconciled", func() {
		BeforeEach(func() {
			// Create a daemonset and wait for it to be reconciled
			clearReconciled()
			m.Create(daemonset).Should(Succeed())
			waitForDaemonSetReconciled(daemonset)
		})

		Context("And it has the required annotation", func() {
			BeforeEach(func() {
				addAnnotation := func(obj client.Object) client.Object {
					annotations := obj.GetAnnotations()
					if annotations == nil {
						annotations = make(map[string]string)
					}
					annotations[core.RequiredAnnotation] = "true"
					obj.SetAnnotations(annotations)
					return obj
				}
				clearReconciled()
				m.Update(daemonset, addAnnotation).Should(Succeed())
				waitForDaemonSetReconciled(daemonset)

				// Get the updated DaemonSet
				m.Get(daemonset, timeout).Should(Succeed())
			})

			It("Has scheduling enabled", func() {
				m.Get(daemonset, timeout).Should(Succeed())
				Expect(daemonset.Spec.Template.Spec.SchedulerName).To(Equal("default-scheduler"))
				Expect(daemonset.ObjectMeta.Annotations).NotTo(HaveKey(core.SchedulingDisabledAnnotation))
			})

			It("Adds a config hash to the Pod Template", func() {
				Eventually(daemonset, timeout).Should(utils.WithPodTemplateAnnotations(HaveKey(core.ConfigHashAnnotation)))
			})

			It("Sends an event when updating the hash", func() {
				eventMessage := func(event *corev1.Event) string {
					return event.Message
				}
				hashMessage := "Configuration hash updated to bd786f47ef9b79841ddba1059752f95c4fe21906df5e2964786b4658e02758d5"
				Eventually(func() *corev1.EventList {
					events := &corev1.EventList{}
					m.Client.List(context.TODO(), events)
					return events
				}, timeout).Should(utils.WithItems(ContainElement(WithTransform(eventMessage, Equal(hashMessage)))))
			})

			Context("And a child is removed", func() {
				var originalHash string
				BeforeEach(func() {
					Eventually(daemonset, timeout).Should(utils.WithPodTemplateAnnotations(HaveKey(core.ConfigHashAnnotation)))
					originalHash = daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]

					// Remove "container2" which references Secret example2 and ConfigMap
					// example2
					removeContainer2 := func(obj client.Object) client.Object {
						ss, _ := obj.(*appsv1.DaemonSet)
						containers := ss.Spec.Template.Spec.Containers
						Expect(containers[0].Name).To(Equal("container1"))
						ss.Spec.Template.Spec.Containers = []corev1.Container{containers[0]}
						return ss
					}
					clearReconciled()
					m.Update(daemonset, removeContainer2).Should(Succeed())
					waitForDaemonSetReconciled(daemonset)

					// Get the updated DaemonSet
					m.Get(daemonset, timeout).Should(Succeed())
				})

				It("Updates the config hash in the Pod Template", func() {
					Eventually(func() string {
						return daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]
					}, timeout).ShouldNot(Equal(originalHash))
				})

				It("Changes to the removed children no longer trigger a reconcile", func() {
					modifyCM := func(obj client.Object) client.Object {
						cm, _ := obj.(*corev1.ConfigMap)
						cm.Data["key1"] = "modified"
						return cm
					}
					clearReconciled()

					m.Update(cm2, modifyCM).Should(Succeed())
					consistentlyDaemonSetNotReconciled(daemonset)
				})
			})

			Context("And a child is updated", func() {
				var originalHash string

				BeforeEach(func() {
					m.Get(daemonset, timeout).Should(Succeed())
					Eventually(daemonset, timeout).Should(utils.WithPodTemplateAnnotations(HaveKey(core.ConfigHashAnnotation)))
					originalHash = daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]
				})

				Context("A ConfigMap volume is updated", func() {
					BeforeEach(func() {
						modifyCM := func(obj client.Object) client.Object {
							cm, _ := obj.(*corev1.ConfigMap)
							cm.Data["key1"] = modified
							return cm
						}
						clearReconciled()
						m.Update(cm1, modifyCM).Should(Succeed())
						waitForDaemonSetReconciled(daemonset)
						waitForDaemonSetReconciled(daemonset)

						// Get the updated DaemonSet
						m.Get(daemonset, timeout).Should(Succeed())
					})

					It("Updates the config hash in the Pod Template", func() {
						Eventually(func() string {
							return daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]
						}, timeout).ShouldNot(Equal(originalHash))
					})
				})

				Context("A ConfigMap EnvSource is updated", func() {
					BeforeEach(func() {
						modifyCM := func(obj client.Object) client.Object {
							cm, _ := obj.(*corev1.ConfigMap)
							cm.Data["key1"] = modified
							return cm
						}
						clearReconciled()
						m.Update(cm2, modifyCM).Should(Succeed())
						waitForDaemonSetReconciled(daemonset)
						waitForDaemonSetReconciled(daemonset)

						// Get the updated DaemonSet
						m.Get(daemonset, timeout).Should(Succeed())
					})

					It("Updates the config hash in the Pod Template", func() {
						Eventually(func() string {
							return daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]
						}, timeout).ShouldNot(Equal(originalHash))
					})
				})

				Context("A Secret volume is updated", func() {
					BeforeEach(func() {
						modifyS := func(obj client.Object) client.Object {
							s, _ := obj.(*corev1.Secret)
							if s.StringData == nil {
								s.StringData = make(map[string]string)
							}
							s.StringData["key1"] = modified
							return s
						}
						clearReconciled()
						m.Update(s1, modifyS).Should(Succeed())
						waitForDaemonSetReconciled(daemonset)
						waitForDaemonSetReconciled(daemonset)

						// Get the updated DaemonSet
						m.Get(daemonset, timeout).Should(Succeed())
					})

					It("Updates the config hash in the Pod Template", func() {
						Eventually(func() string {
							return daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]
						}, timeout).ShouldNot(Equal(originalHash))
					})
				})

				Context("A Secret EnvSource is updated", func() {
					BeforeEach(func() {
						modifyS := func(obj client.Object) client.Object {
							s, _ := obj.(*corev1.Secret)
							if s.StringData == nil {
								s.StringData = make(map[string]string)
							}
							s.StringData["key1"] = modified
							return s
						}
						clearReconciled()
						m.Update(s2, modifyS).Should(Succeed())
						waitForDaemonSetReconciled(daemonset)
						waitForDaemonSetReconciled(daemonset)

						// Get the updated DaemonSet
						m.Get(daemonset, timeout).Should(Succeed())
					})

					It("Updates the config hash in the Pod Template", func() {
						Eventually(func() string {
							return daemonset.Spec.Template.GetAnnotations()[core.ConfigHashAnnotation]
						}, timeout).ShouldNot(Equal(originalHash))
					})
				})
			})

			Context("And the annotation is removed", func() {
				BeforeEach(func() {
					removeAnnotations := func(obj client.Object) client.Object {
						obj.SetAnnotations(make(map[string]string))
						return obj
					}
					clearReconciled()
					m.Update(daemonset, removeAnnotations).Should(Succeed())
					waitForDaemonSetReconciled(daemonset)
					m.Get(daemonset).Should(Succeed())
					Eventually(daemonset, timeout).ShouldNot(utils.WithAnnotations(HaveKey(core.RequiredAnnotation)))
				})

				It("Removes the config hash annotation", func() {
					m.Consistently(daemonset, consistentlyTimeout).ShouldNot(utils.WithAnnotations(ContainElement(core.ConfigHashAnnotation)))
				})

				It("Changes to children no longer trigger a reconcile", func() {
					modifyCM := func(obj client.Object) client.Object {
						cm, _ := obj.(*corev1.ConfigMap)
						cm.Data["key1"] = "modified"
						return cm
					}
					clearReconciled()

					m.Update(cm1, modifyCM).Should(Succeed())
					consistentlyDaemonSetNotReconciled(daemonset)
				})
			})

			Context("And is deleted", func() {
				BeforeEach(func() {
					// Make sure the cache has synced before we run the test
					Eventually(daemonset, timeout).Should(utils.WithPodTemplateAnnotations(HaveKey(core.ConfigHashAnnotation)))
					clearReconciled()
					m.Delete(daemonset).Should(Succeed())
					waitForDaemonSetReconciled(daemonset)
				})

				It("Not longer exists", func() {
					m.Get(daemonset).Should(MatchError(MatchRegexp(`not found`)))
				})

				It("Changes to children no longer trigger a reconcile", func() {
					modifyCM := func(obj client.Object) client.Object {
						cm, _ := obj.(*corev1.ConfigMap)
						cm.Data["key1"] = "modified"
						return cm
					}
					clearReconciled()

					m.Update(cm1, modifyCM).Should(Succeed())
					consistentlyDaemonSetNotReconciled(daemonset)
				})
			})
		})

		Context("And it does not have the required annotation", func() {
			BeforeEach(func() {
				// Get the updated DaemonSet
				m.Get(daemonset, timeout).Should(Succeed())
			})

			It("Doesn't add a config hash to the Pod Template", func() {
				m.Consistently(daemonset, consistentlyTimeout).ShouldNot(utils.WithAnnotations(ContainElement(core.ConfigHashAnnotation)))
			})

			It("Changes to children no do not trigger a reconcile", func() {
				modifyCM := func(obj client.Object) client.Object {
					cm, _ := obj.(*corev1.ConfigMap)
					cm.Data["key1"] = "modified"
					return cm
				}
				clearReconciled()

				m.Update(cm1, modifyCM).Should(Succeed())
				consistentlyDaemonSetNotReconciled(daemonset)
			})
		})
	})

	Context("When a DaemonSet with missing children is reconciled", func() {
		BeforeEach(func() {
			m.Delete(cm1).Should(Succeed())

			annotations := daemonset.GetAnnotations()
			if annotations == nil {
				annotations = make(map[string]string)
			}
			annotations[core.RequiredAnnotation] = "true"
			daemonset.SetAnnotations(annotations)

			// Create a daemonset and wait for it to be reconciled
			clearReconciled()
			m.Create(daemonset).Should(Succeed())
			waitForDaemonSetReconciled(daemonset)
		})

		It("Has scheduling disabled", func() {
			m.Get(daemonset, timeout).Should(Succeed())
			Expect(daemonset.Spec.Template.Spec.SchedulerName).To(Equal(core.SchedulingDisabledSchedulerName))
			Expect(daemonset.ObjectMeta.Annotations[core.SchedulingDisabledAnnotation]).To(Equal("default-scheduler"))
		})

		Context("And the missing child is created", func() {
			BeforeEach(func() {
				clearReconciled()
				cm1 = utils.ExampleConfigMap1.DeepCopy()
				m.Create(cm1).Should(Succeed())
				waitForDaemonSetReconciled(daemonset)
			})

			It("Has scheduling renabled", func() {
				m.Get(daemonset, timeout).Should(Succeed())
				Expect(daemonset.Spec.Template.Spec.SchedulerName).To(Equal("default-scheduler"))
				Expect(daemonset.ObjectMeta.Annotations).NotTo(HaveKey(core.SchedulingDisabledAnnotation))
			})
		})
	})
})
