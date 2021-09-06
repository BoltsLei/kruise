/*
Copyright 2021 The Kruise Authors.

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

package apps

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/utils/pointer"
	"time"

	appsv1alpha1 "github.com/openkruise/kruise/apis/apps/v1alpha1"
	kruiseclientset "github.com/openkruise/kruise/pkg/client/clientset/versioned"
	"github.com/openkruise/kruise/pkg/util/workloadspread"
	"github.com/openkruise/kruise/test/e2e/framework"
)

var (
	KruiseKindCloneSet = appsv1alpha1.SchemeGroupVersion.WithKind("CloneSet")
	//controllerKindDep  = appsv1.SchemeGroupVersion.WithKind("Deployment")
	//controllerKindJob  = batchv1.SchemeGroupVersion.WithKind("Job")
)

var _ = SIGDescribe("workloadspread", func() {
	f := framework.NewDefaultFramework("workloadspread")
	workloadSpreadName := "test-workload-spread"
	var ns string
	var c clientset.Interface
	var kc kruiseclientset.Interface
	var tester *framework.WorkloadSpreadTester

	ginkgo.BeforeEach(func() {
		c = f.ClientSet
		kc = f.KruiseClientSet
		ns = f.Namespace.Name
		tester = framework.NewWorkloadSpreadTester(c, kc)
	})

	framework.KruiseDescribe("WorkloadSpread functionality", func() {
		ginkgo.AfterEach(func() {
			if ginkgo.CurrentGinkgoTestDescription().Failed {
				framework.DumpDebugInfo(c, ns)
			}
		})

		ginkgo.It("deploy in two zone, the type of maxReplicas is Integer", func() {
			cloneSet := tester.NewBaseCloneSet(ns)
			// create workloadSpread
			targetRef := appsv1alpha1.TargetReference{
				APIVersion: KruiseKindCloneSet.GroupVersion().String(),
				Kind:       KruiseKindCloneSet.Kind,
				Name:       cloneSet.Name,
			}
			subset1 := appsv1alpha1.WorkloadSpreadSubset{
				Name: "ack",
				RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"ack"},
						},
					},
				},
				MaxReplicas: &intstr.IntOrString{Type: intstr.Int, IntVal: 3},
				Patch: runtime.RawExtension{
					Raw: []byte(`{"metadata":{"annotations":{"subset":"ack"}}}`),
				},
			}
			subset2 := appsv1alpha1.WorkloadSpreadSubset{
				Name: "eci",
				RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"eci"},
						},
					},
				},
				MaxReplicas: &intstr.IntOrString{Type: intstr.Int, IntVal: 3},
				Patch: runtime.RawExtension{
					Raw: []byte(`{"metadata":{"annotations":{"subset":"eci"}}}`),
				},
			}
			workloadSpread := tester.NewWorkloadSpread(ns, workloadSpreadName, &targetRef, []appsv1alpha1.WorkloadSpreadSubset{subset1, subset2})
			workloadSpread = tester.CreateWorkloadSpread(workloadSpread)

			// create cloneset, replicas = 6
			cloneSet.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
			cloneSet.Spec.Replicas = pointer.Int32Ptr(6)
			cloneSet = tester.CreateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err := tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(6))
			subset1Pods := 0
			subset2Pods := 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(3))
			gomega.Expect(subset2Pods).To(gomega.Equal(3))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(3)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(3)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			// update cloneset image
			ginkgo.By(fmt.Sprintf("update cloneSet(%s/%s) image=%s", cloneSet.Namespace, cloneSet.Name, "nginx:alpine"))
			cloneSet.Spec.Template.Spec.Containers[0].Image = "nginx:alpine"
			tester.UpdateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(6))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(3))
			gomega.Expect(subset2Pods).To(gomega.Equal(3))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(3)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(3)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			//scale down cloneSet.replicas = 4, maxReplicas = 2.
			ginkgo.By(fmt.Sprintf("scale up cloneSet(%s/%s) replicas=4", cloneSet.Namespace, cloneSet.Name))
			workloadSpread.Spec.Subsets[0].MaxReplicas.IntVal = 2
			workloadSpread.Spec.Subsets[1].MaxReplicas.IntVal = 2
			tester.UpdateWorkloadSpread(workloadSpread)

			cloneSet.Spec.Replicas = pointer.Int32Ptr(4)
			tester.UpdateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(4))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(2))
			gomega.Expect(subset2Pods).To(gomega.Equal(2))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			//scale up cloneSet.replicas = 8
			ginkgo.By(fmt.Sprintf("scale up cloneSet(%s/%s) replicas=8, maxReplicas=4", cloneSet.Namespace, cloneSet.Name))
			workloadSpread.Spec.Subsets[0].MaxReplicas.IntVal = 4
			workloadSpread.Spec.Subsets[1].MaxReplicas.IntVal = 4
			tester.UpdateWorkloadSpread(workloadSpread)
			cloneSet.Spec.Replicas = pointer.Int32Ptr(8)
			tester.UpdateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(8))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(4))
			gomega.Expect(subset2Pods).To(gomega.Equal(4))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(4)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(4)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			ginkgo.By("deploy in two zone, the type of maxReplicas is Integer, done")
		})

		ginkgo.It("elastic deployment, ack=2, eci=nil", func() {
			cloneSet := tester.NewBaseCloneSet(ns)
			// create workloadSpread
			targetRef := appsv1alpha1.TargetReference{
				APIVersion: KruiseKindCloneSet.GroupVersion().String(),
				Kind:       KruiseKindCloneSet.Kind,
				Name:       cloneSet.Name,
			}
			subset1 := appsv1alpha1.WorkloadSpreadSubset{
				Name: "ack",
				RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"ack"},
						},
					},
				},
				MaxReplicas: &intstr.IntOrString{Type: intstr.Int, IntVal: 2},
				Patch: runtime.RawExtension{
					Raw: []byte(`{"metadata":{"annotations":{"subset":"ack"}}}`),
				},
			}
			subset2 := appsv1alpha1.WorkloadSpreadSubset{
				Name: "eci",
				RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"eci"},
						},
					},
				},
				MaxReplicas: nil,
				Patch: runtime.RawExtension{
					Raw: []byte(`{"metadata":{"annotations":{"subset":"eci"}}}`),
				},
			}
			workloadSpread := tester.NewWorkloadSpread(ns, workloadSpreadName, &targetRef, []appsv1alpha1.WorkloadSpreadSubset{subset1, subset2})
			workloadSpread = tester.CreateWorkloadSpread(workloadSpread)

			// create cloneset, replicas = 2
			cloneSet.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
			cloneSet = tester.CreateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err := tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(2))
			subset1Pods := 0
			subset2Pods := 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(2))
			gomega.Expect(subset2Pods).To(gomega.Equal(0))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(-1)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(0)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			//scale up cloneSet.replicas = 6
			ginkgo.By(fmt.Sprintf("scale up cloneSet(%s/%s) replicas=6", cloneSet.Namespace, cloneSet.Name))
			cloneSet.Spec.Replicas = pointer.Int32Ptr(6)
			tester.UpdateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(6))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(2))
			gomega.Expect(subset2Pods).To(gomega.Equal(4))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(-1)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(4)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			// update cloneset image
			ginkgo.By(fmt.Sprintf("update cloneSet(%s/%s) image=%s", cloneSet.Namespace, cloneSet.Name, "nginx:alpine"))
			cloneSet.Spec.Template.Spec.Containers[0].Image = "nginx:alpine"
			tester.UpdateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(6))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(2))
			gomega.Expect(subset2Pods).To(gomega.Equal(4))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(-1)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(4)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			//scale down cloneSet.replicas = 2
			ginkgo.By(fmt.Sprintf("scale down cloneSet(%s/%s) replicas=2", cloneSet.Namespace, cloneSet.Name))
			cloneSet.Spec.Replicas = pointer.Int32Ptr(2)
			tester.UpdateCloneSet(cloneSet)
			tester.WaitForCloneSetRunning(cloneSet)

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(2))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(2))
			gomega.Expect(subset2Pods).To(gomega.Equal(0))

			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(-1)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(0)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))

			ginkgo.By("elastic deployment, ack=2, eci=nil, done")
		})

		ginkgo.It("reschedule subset-a", func() {
			cloneSet := tester.NewBaseCloneSet(ns)
			// create workloadSpread
			targetRef := appsv1alpha1.TargetReference{
				APIVersion: KruiseKindCloneSet.GroupVersion().String(),
				Kind:       KruiseKindCloneSet.Kind,
				Name:       cloneSet.Name,
			}
			subset1 := appsv1alpha1.WorkloadSpreadSubset{
				Name: "subset-a",
				RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							// Pod is not schedulable due to incorrect configuration
							Values: []string{"asi"},
						},
					},
				},
				MaxReplicas: &intstr.IntOrString{Type: intstr.Int, IntVal: 2},
				Patch: runtime.RawExtension{
					Raw: []byte(`{"metadata":{"annotations":{"subset":"subset-a"}}}`),
				},
			}
			subset2 := appsv1alpha1.WorkloadSpreadSubset{
				Name: "subset-b",
				RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      "topology.kubernetes.io/zone",
							Operator: corev1.NodeSelectorOpIn,
							Values:   []string{"eci"},
						},
					},
				},
				Patch: runtime.RawExtension{
					Raw: []byte(`{"metadata":{"annotations":{"subset":"subset-b"}}}`),
				},
			}
			workloadSpread := tester.NewWorkloadSpread(ns, workloadSpreadName, &targetRef, []appsv1alpha1.WorkloadSpreadSubset{subset1, subset2})
			workloadSpread.Spec.ScheduleStrategy = appsv1alpha1.WorkloadSpreadScheduleStrategy{
				Type: appsv1alpha1.FixedWorkloadSpreadScheduleStrategyType,
			}
			workloadSpread = tester.CreateWorkloadSpread(workloadSpread)

			// create cloneset, replicas = 5
			cloneSet.Spec.Replicas = pointer.Int32Ptr(5)
			cloneSet.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
			cloneSet = tester.CreateCloneSet(cloneSet)
			tester.WaitForCloneSetRunReplicas(cloneSet, int32(3))

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			pods, err := tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(5))
			subset1Pods := 0
			subset1RunningPods := 0
			subset2Pods := 0
			subset2RunningPods := 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
						if pod.Status.Phase == corev1.PodRunning {
							subset1RunningPods++
						}
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
						gomega.Expect(pod.Annotations["subset"]).To(gomega.Equal(subset1.Name))
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						if pod.Status.Phase == corev1.PodRunning {
							subset2RunningPods++
						}
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
						gomega.Expect(pod.Annotations["subset"]).To(gomega.Equal(subset2.Name))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(2))
			gomega.Expect(subset1RunningPods).To(gomega.Equal(0))
			gomega.Expect(subset2Pods).To(gomega.Equal(3))
			gomega.Expect(subset2RunningPods).To(gomega.Equal(3))

			// check workloadSpread status
			ginkgo.By(fmt.Sprintf("check workloadSpread(%s/%s) status", workloadSpread.Namespace, workloadSpread.Name))
			workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Replicas).To(gomega.Equal(int32(2)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].Conditions)).To(gomega.Equal(0))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(-1)))
			gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Replicas).To(gomega.Equal(int32(3)))
			gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].Conditions)).To(gomega.Equal(0))

			// wait for subset schedulabe
			ginkgo.By(fmt.Sprintf("wait workloadSpread(%s/%s) subset-a reschedulabe", workloadSpread.Namespace, workloadSpread.Name))
			workloadSpread.Spec.ScheduleStrategy = appsv1alpha1.WorkloadSpreadScheduleStrategy{
				Type: appsv1alpha1.AdaptiveWorkloadSpreadScheduleStrategyType,
				Adaptive: &appsv1alpha1.AdaptiveWorkloadSpreadStrategy{
					DisableSimulationSchedule: true,
					RescheduleCriticalSeconds: pointer.Int32Ptr(5),
				},
			}
			tester.UpdateWorkloadSpread(workloadSpread)
			tester.WaitForWorkloadSpreadRunning(workloadSpread)

			err = wait.PollImmediate(time.Second, time.Minute*6, func() (bool, error) {
				ws, err := kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				for _, condition := range ws.Status.SubsetStatuses[0].Conditions {
					if condition.Type == appsv1alpha1.SubsetSchedulable && condition.Status == corev1.ConditionFalse {
						return true, nil
					}
				}
				return false, nil
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			// get pods, and check workloadSpread
			ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
			tester.WaitForCloneSetRunReplicas(cloneSet, int32(5))
			pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(pods).To(gomega.HaveLen(5))
			subset1Pods = 0
			subset2Pods = 0
			for _, pod := range pods {
				if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
					var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
					err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
					gomega.Expect(err).NotTo(gomega.HaveOccurred())
					if injectWorkloadSpread.Subset == subset1.Name {
						subset1Pods++
					} else if injectWorkloadSpread.Subset == subset2.Name {
						subset2Pods++
						gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
						gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
						gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
						gomega.Expect(pod.Annotations["subset"]).To(gomega.Equal(subset2.Name))
					}
				} else {
					// others PodDeletionCostAnnotation not set
					gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
				}
			}
			gomega.Expect(subset1Pods).To(gomega.Equal(0))
			gomega.Expect(subset2Pods).To(gomega.Equal(5))

			// wait subset-a to schedulable
			ginkgo.By(fmt.Sprintf("wait subset-a to schedulable"))
			err = wait.PollImmediate(time.Second, time.Minute*5, func() (bool, error) {
				ws, err := kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(context.TODO(), workloadSpread.Name, metav1.GetOptions{})
				if err != nil {
					return false, err
				}
				for _, condition := range ws.Status.SubsetStatuses[0].Conditions {
					if condition.Type == appsv1alpha1.SubsetSchedulable && condition.Status == corev1.ConditionTrue {
						return true, nil
					}
				}
				return false, nil
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			ginkgo.By("workloadSpread reschedule subset-a, done")
		})

		//ginkgo.It("deploy in two zone, maxReplicas=50%", func() {
		//	cloneSet := tester.NewBaseCloneSet(ns)
		//	// create workloadSpread
		//	targetRef := appsv1alpha1.TargetReference{
		//		APIVersion: KruiseKindCloneSet.GroupVersion().String(),
		//		Kind:       KruiseKindCloneSet.Kind,
		//		Name:       cloneSet.Name,
		//	}
		//	subset1 := appsv1alpha1.WorkloadSpreadSubset{
		//		Name: "ack",
		//		RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
		//			MatchExpressions: []corev1.NodeSelectorRequirement{
		//				{
		//					Key:      "topology.kubernetes.io/zone",
		//					Operator: corev1.NodeSelectorOpIn,
		//					Values:   []string{"ack"},
		//				},
		//			},
		//		},
		//		MaxReplicas: &intstr.IntOrString{Type: intstr.String, StrVal: "50%"},
		//		Patch: runtime.RawExtension{
		//			Raw: []byte(`{"metadata":{"annotations":{"subset":"ack"}}}`),
		//		},
		//	}
		//	subset2 := appsv1alpha1.WorkloadSpreadSubset{
		//		Name: "eci",
		//		RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
		//			MatchExpressions: []corev1.NodeSelectorRequirement{
		//				{
		//					Key:      "topology.kubernetes.io/zone",
		//					Operator: corev1.NodeSelectorOpIn,
		//					Values:   []string{"eci"},
		//				},
		//			},
		//		},
		//		MaxReplicas: &intstr.IntOrString{Type: intstr.String, StrVal: "50%"},
		//		Patch: runtime.RawExtension{
		//			Raw: []byte(`{"metadata":{"annotations":{"subset":"eci"}}}`),
		//		},
		//	}
		//	workloadSpread := tester.NewWorkloadSpread(ns, workloadSpreadName, &targetRef, []appsv1alpha1.WorkloadSpreadSubset{subset1, subset2})
		//	workloadSpread = tester.CreateWorkloadSpread(workloadSpread)
		//
		//	// create cloneset, replicas = 2
		//	cloneSet.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
		//	cloneSet = tester.CreateCloneSet(cloneSet)
		//	tester.WaitForCloneSetRunning(cloneSet)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err := tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(2))
		//	subset1Pods := 0
		//	subset2Pods := 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(1))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(1))
		//
		//	// update cloneset image
		//	ginkgo.By(fmt.Sprintf("update cloneSet(%s/%s) image=%s", cloneSet.Namespace, cloneSet.Name, "nginx:alpine"))
		//	cloneSet.Spec.Template.Spec.Containers[0].Image = "nginx:alpine"
		//	tester.UpdateCloneSet(cloneSet)
		//	tester.WaitForCloneSetRunning(cloneSet)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(2))
		//	subset1Pods = 0
		//	subset2Pods = 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(1))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(1))
		//
		//	//scale up cloneSet.replicas = 6
		//	ginkgo.By(fmt.Sprintf("scale up cloneSet(%s/%s) replicas=6", cloneSet.Namespace, cloneSet.Name))
		//	cloneSet.Spec.Replicas = pointer.Int32Ptr(6)
		//	tester.UpdateCloneSet(cloneSet)
		//	tester.WaitForCloneSetRunning(cloneSet)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(6))
		//	subset1Pods = 0
		//	subset2Pods = 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(3))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(3))
		//
		//	workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(workloadSpread.Name, metav1.GetOptions{})
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
		//
		//	//scale down cloneSet.replicas = 2
		//	ginkgo.By(fmt.Sprintf("scale down cloneSet(%s/%s) replicas=2", cloneSet.Namespace, cloneSet.Name))
		//	cloneSet.Spec.Replicas = pointer.Int32Ptr(2)
		//	tester.UpdateCloneSet(cloneSet)
		//	tester.WaitForCloneSetRunning(cloneSet)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get cloneSet(%s/%s) pods, and check workloadSpread(%s/%s) status", cloneSet.Namespace, cloneSet.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err = tester.GetSelectorPods(cloneSet.Namespace, cloneSet.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(2))
		//	subset1Pods = 0
		//	subset2Pods = 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(1))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(1))
		//
		//	ginkgo.By("deploy in two zone, maxReplicas=50%, done")
		//})

		// test k8s cluster version >= 1.21
		//ginkgo.It("elastic deploy for deployment, ack=2, eci=nil", func() {
		//	deployment := tester.NewBaseDeployment(ns)
		//	// create workloadSpread
		//	targetRef := appsv1alpha1.TargetReference{
		//		APIVersion: controllerKindDep.GroupVersion().String(),
		//		Kind:       controllerKindDep.Kind,
		//		Name:       deployment.Name,
		//	}
		//	subset1 := appsv1alpha1.WorkloadSpreadSubset{
		//		Name: "ack",
		//		RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
		//			MatchExpressions: []corev1.NodeSelectorRequirement{
		//				{
		//					Key:      "topology.kubernetes.io/zone",
		//					Operator: corev1.NodeSelectorOpIn,
		//					Values:   []string{"ack"},
		//				},
		//			},
		//		},
		//		MaxReplicas: &intstr.IntOrString{Type: intstr.Int, IntVal: 2},
		//		Patch: runtime.RawExtension{
		//			Raw: []byte(`{"metadata":{"annotations":{"subset":"ack"}}}`),
		//		},
		//	}
		//	subset2 := appsv1alpha1.WorkloadSpreadSubset{
		//		Name: "eci",
		//		RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
		//			MatchExpressions: []corev1.NodeSelectorRequirement{
		//				{
		//					Key:      "topology.kubernetes.io/zone",
		//					Operator: corev1.NodeSelectorOpIn,
		//					Values:   []string{"eci"},
		//				},
		//			},
		//		},
		//		MaxReplicas: nil,
		//		Patch: runtime.RawExtension{
		//			Raw: []byte(`{"metadata":{"annotations":{"subset":"eci"}}}`),
		//		},
		//	}
		//	workloadSpread := tester.NewWorkloadSpread(ns, workloadSpreadName, &targetRef, []appsv1alpha1.WorkloadSpreadSubset{subset1, subset2})
		//	workloadSpread = tester.CreateWorkloadSpread(workloadSpread)
		//
		//	// create deployment, replicas = 2
		//	deployment.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
		//	deployment = tester.CreateDeployment(deployment)
		//	tester.WaitForDeploymentRunning(deployment)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get deployment(%s/%s) pods, and check workloadSpread(%s/%s) status", deployment.Namespace, deployment.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err := tester.GetSelectorPods(deployment.Namespace, deployment.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(2))
		//	subset1Pods := 0
		//	subset2Pods := 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(2))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(0))
		//
		//	//scale up deployment.replicas = 6
		//	ginkgo.By(fmt.Sprintf("scale up deployment(%s/%s) replicas=6", deployment.Namespace, deployment.Name))
		//	deployment.Spec.Replicas = pointer.Int32Ptr(6)
		//	tester.UpdateDeployment(deployment)
		//	tester.WaitForDeploymentRunning(deployment)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get deployment(%s/%s) pods, and check workloadSpread(%s/%s) status", deployment.Namespace, deployment.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err = tester.GetSelectorPods(deployment.Namespace, deployment.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(6))
		//	subset1Pods = 0
		//	subset2Pods = 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(2))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(4))
		//
		//	workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(workloadSpread.Name, metav1.GetOptions{})
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(0)))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
		//
		//	// update deployment image
		//	ginkgo.By(fmt.Sprintf("update deployment(%s/%s) image=%s", deployment.Namespace, deployment.Name, "nginx:alpine"))
		//	deployment.Spec.Template.Spec.Containers[0].Image = "nginx:alpine"
		//	tester.UpdateDeployment(deployment)
		//	tester.WaitForDeploymentRunning(deployment)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get deployment(%s/%s) pods, and check workloadSpread(%s/%s) status", deployment.Namespace, deployment.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err = tester.GetSelectorPods(deployment.Namespace, deployment.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(6))
		//	subset1Pods = 0
		//	subset2Pods = 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(2))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(4))
		//
		//	//scale down deployment.replicas = 2
		//	ginkgo.By(fmt.Sprintf("scale down deployment(%s/%s) replicas=2", deployment.Namespace, deployment.Name))
		//	deployment.Spec.Replicas = pointer.Int32Ptr(2)
		//	tester.UpdateDeployment(deployment)
		//	tester.WaitForDeploymentRunning(deployment)
		//
		//	time.Sleep(10 * time.Minute)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get deployment(%s/%s) pods, and check workloadSpread(%s/%s) status", deployment.Namespace, deployment.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	pods, err = tester.GetSelectorPods(deployment.Namespace, deployment.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	gomega.Expect(pods).To(gomega.HaveLen(2))
		//	subset1Pods = 0
		//	subset2Pods = 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("200"))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal("100"))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(2))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(0))
		//
		//	ginkgo.By("elastic deploy for deployment, ack=2, eci=nil, done")
		//})

		//ginkgo.It("deploy for job, ack=1, eci=nil", func() {
		//	job := tester.NewBaseJob(ns)
		//	// create workloadSpread
		//	targetRef := appsv1alpha1.TargetReference{
		//		APIVersion: controllerKindJob.GroupVersion().String(),
		//		Kind:       controllerKindJob.Kind,
		//		Name:       job.Name,
		//	}
		//	subset1 := appsv1alpha1.WorkloadSpreadSubset{
		//		Name: "ack",
		//		RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
		//			MatchExpressions: []corev1.NodeSelectorRequirement{
		//				{
		//					Key:      "topology.kubernetes.io/zone",
		//					Operator: corev1.NodeSelectorOpIn,
		//					Values:   []string{"ack"},
		//				},
		//			},
		//		},
		//		MaxReplicas: &intstr.IntOrString{Type: intstr.Int, IntVal: 1},
		//		Patch: runtime.RawExtension{
		//			Raw: []byte(`{"metadata":{"annotations":{"subset":"ack"}}}`),
		//		},
		//	}
		//	subset2 := appsv1alpha1.WorkloadSpreadSubset{
		//		Name: "eci",
		//		RequiredNodeSelectorTerm: &corev1.NodeSelectorTerm{
		//			MatchExpressions: []corev1.NodeSelectorRequirement{
		//				{
		//					Key:      "topology.kubernetes.io/zone",
		//					Operator: corev1.NodeSelectorOpIn,
		//					Values:   []string{"eci"},
		//				},
		//			},
		//		},
		//		Patch: runtime.RawExtension{
		//			Raw: []byte(`{"metadata":{"annotations":{"subset":"eci"}}}`),
		//		},
		//	}
		//	workloadSpread := tester.NewWorkloadSpread(ns, workloadSpreadName, &targetRef, []appsv1alpha1.WorkloadSpreadSubset{subset1, subset2})
		//	workloadSpread = tester.CreateWorkloadSpread(workloadSpread)
		//
		//	job.Spec.Completions = pointer.Int32Ptr(10)
		//	job.Spec.Parallelism = pointer.Int32Ptr(2)
		//	job.Spec.Template.Spec.Containers[0].Image = "busybox:latest"
		//	job.Spec.Template.Spec.RestartPolicy = corev1.RestartPolicyNever
		//	job = tester.CreateJob(job)
		//	tester.WaitJobCompleted(job)
		//
		//	// get pods, and check workloadSpread
		//	ginkgo.By(fmt.Sprintf("get job(%s/%s) pods, and check workloadSpread(%s/%s) status", job.Namespace, job.Name, workloadSpread.Namespace, workloadSpread.Name))
		//	faster, err := util.GetFastLabelSelector(job.Spec.Selector)
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//	podList, err := tester.C.CoreV1().Pods(job.Namespace).List(metav1.ListOptions{LabelSelector: faster.String()})
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//
		//	matchedPods := make([]corev1.Pod, 0, len(podList.Items))
		//	for i := range podList.Items {
		//		if podList.Items[i].Status.Phase == corev1.PodSucceeded {
		//			matchedPods = append(matchedPods, podList.Items[i])
		//		}
		//	}
		//
		//	pods := matchedPods
		//	gomega.Expect(pods).To(gomega.HaveLen(10))
		//	subset1Pods := 0
		//	subset2Pods := 0
		//	for _, pod := range pods {
		//		if str, ok := pod.Annotations[workloadspread.MatchedWorkloadSpreadSubsetAnnotations]; ok {
		//			var injectWorkloadSpread *workloadspread.InjectWorkloadSpread
		//			err := json.Unmarshal([]byte(str), &injectWorkloadSpread)
		//			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//			if injectWorkloadSpread.Subset == subset1.Name {
		//				subset1Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset1.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations["subset"]).To(gomega.Equal(subset1.Name))
		//			} else if injectWorkloadSpread.Subset == subset2.Name {
		//				subset2Pods++
		//				gomega.Expect(injectWorkloadSpread.Name).To(gomega.Equal(workloadSpread.Name))
		//				gomega.Expect(pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions).To(gomega.Equal(subset2.RequiredNodeSelectorTerm.MatchExpressions))
		//				gomega.Expect(pod.Annotations["subset"]).To(gomega.Equal(subset2.Name))
		//			}
		//		} else {
		//			// others PodDeletionCostAnnotation not set
		//			gomega.Expect(pod.Annotations[workloadspread.PodDeletionCostAnnotation]).To(gomega.Equal(""))
		//		}
		//	}
		//	gomega.Expect(subset1Pods).To(gomega.Equal(5))
		//	gomega.Expect(subset2Pods).To(gomega.Equal(5))
		//
		//	// check workloadSpread status
		//	ginkgo.By(fmt.Sprintf("check workloadSpread(%s/%s) status", workloadSpread.Namespace, workloadSpread.Name))
		//	workloadSpread, err = kc.AppsV1alpha1().WorkloadSpreads(workloadSpread.Namespace).Get(workloadSpread.Name, metav1.GetOptions{})
		//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
		//
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[0].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[0].Name))
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[0].MissingReplicas).To(gomega.Equal(int32(1)))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].CreatingPods)).To(gomega.Equal(0))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[0].DeletingPods)).To(gomega.Equal(0))
		//
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[1].Name).To(gomega.Equal(workloadSpread.Spec.Subsets[1].Name))
		//	gomega.Expect(workloadSpread.Status.SubsetStatuses[1].MissingReplicas).To(gomega.Equal(int32(-1)))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].CreatingPods)).To(gomega.Equal(0))
		//	gomega.Expect(len(workloadSpread.Status.SubsetStatuses[1].DeletingPods)).To(gomega.Equal(0))
		//
		//	ginkgo.By("workloadSpread for job, done")
		//})

	})
})
