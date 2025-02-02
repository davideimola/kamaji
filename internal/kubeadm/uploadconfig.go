// Copyright 2022 Clastix Labs
// SPDX-License-Identifier: Apache-2.0

package kubeadm

import (
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sversion "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/kubernetes"
	kubelettypes "k8s.io/kubelet/config/v1beta1"
	kubeadmconstants "k8s.io/kubernetes/cmd/kubeadm/app/constants"
	"k8s.io/kubernetes/cmd/kubeadm/app/phases/uploadconfig"
	"k8s.io/kubernetes/cmd/kubeadm/app/util/apiclient"
	"k8s.io/kubernetes/pkg/apis/rbac"
	"k8s.io/utils/pointer"
)

func UploadKubeadmConfig(client kubernetes.Interface, config *Configuration) error {
	return uploadconfig.UploadConfiguration(&config.InitConfiguration, client)
}

func UploadKubeletConfig(client kubernetes.Interface, config *Configuration) error {
	kubeletConfiguration := KubeletConfiguration{
		TenantControlPlaneDomain:        config.InitConfiguration.Networking.DNSDomain,
		TenantControlPlaneDNSServiceIPs: config.Parameters.TenantDNSServiceIPs,
		TenantControlPlaneCgroupDriver:  config.Parameters.TenantControlPlaneCGroupDriver,
	}
	content, err := getKubeletConfigmapContent(kubeletConfiguration)
	if err != nil {
		return err
	}

	configMapName, err := configMapName(config.Parameters.TenantControlPlaneVersion)
	if err != nil {
		return err
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: metav1.NamespaceSystem,
		},
		Data: map[string]string{
			kubeadmconstants.KubeletBaseConfigurationConfigMapKey: string(content),
		},
	}

	if err := apiclient.CreateOrUpdateConfigMap(client, configMap); err != nil {
		return err
	}

	if err := createConfigMapRBACRules(client, config.Parameters.TenantControlPlaneVersion); err != nil {
		return errors.Wrap(err, "error creating kubelet configuration configmap RBAC rules")
	}

	return nil
}

func getKubeletConfigmapContent(kubeletConfiguration KubeletConfiguration) ([]byte, error) {
	zeroDuration := metav1.Duration{Duration: 0}

	kc := kubelettypes.KubeletConfiguration{
		TypeMeta: metav1.TypeMeta{
			Kind:       "KubeletConfiguration",
			APIVersion: "kubelet.config.k8s.io/v1beta1",
		},
		Authentication: kubelettypes.KubeletAuthentication{
			Anonymous: kubelettypes.KubeletAnonymousAuthentication{
				Enabled: pointer.Bool(false),
			},
			Webhook: kubelettypes.KubeletWebhookAuthentication{
				Enabled:  pointer.Bool(true),
				CacheTTL: zeroDuration,
			},
			X509: kubelettypes.KubeletX509Authentication{
				ClientCAFile: "/etc/kubernetes/pki/ca.crt",
			},
		},
		Authorization: kubelettypes.KubeletAuthorization{
			Mode: kubelettypes.KubeletAuthorizationModeWebhook,
			Webhook: kubelettypes.KubeletWebhookAuthorization{
				CacheAuthorizedTTL:   zeroDuration,
				CacheUnauthorizedTTL: zeroDuration,
			},
		},
		CgroupDriver:              kubeletConfiguration.TenantControlPlaneCgroupDriver,
		ClusterDNS:                kubeletConfiguration.TenantControlPlaneDNSServiceIPs,
		ClusterDomain:             kubeletConfiguration.TenantControlPlaneDomain,
		CPUManagerReconcilePeriod: zeroDuration,
		EvictionHard: map[string]string{
			"imagefs.available": "0%",
			"nodefs.available":  "0%",
			"nodefs.inodesFree": "0%",
		},
		EvictionPressureTransitionPeriod: zeroDuration,
		FileCheckFrequency:               zeroDuration,
		HealthzBindAddress:               "127.0.0.1",
		HealthzPort:                      pointer.Int32(10248),
		HTTPCheckFrequency:               zeroDuration,
		ImageGCHighThresholdPercent:      pointer.Int32(100),
		NodeStatusUpdateFrequency:        zeroDuration,
		NodeStatusReportFrequency:        zeroDuration,
		RotateCertificates:               true,
		RuntimeRequestTimeout:            zeroDuration,
		ShutdownGracePeriod:              zeroDuration,
		ShutdownGracePeriodCriticalPods:  zeroDuration,
		StaticPodPath:                    "/etc/kubernetes/manifests",
		StreamingConnectionIdleTimeout:   zeroDuration,
		SyncFrequency:                    zeroDuration,
		VolumeStatsAggPeriod:             zeroDuration,
	}

	return EncondeToYaml(&kc)
}

func createConfigMapRBACRules(client kubernetes.Interface, kubernetesVersion string) error {
	configMapName, err := configMapName(kubernetesVersion)
	if err != nil {
		return err
	}

	configMapRBACName, err := configMapRBACName(kubernetesVersion)
	if err != nil {
		return err
	}

	if err := apiclient.CreateOrUpdateRole(client, &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapRBACName,
			Namespace: metav1.NamespaceSystem,
		},
		Rules: []rbacv1.PolicyRule{
			{
				Verbs:         []string{"get"},
				APIGroups:     []string{""},
				Resources:     []string{"configmaps"},
				ResourceNames: []string{configMapName},
			},
		},
	}); err != nil {
		return err
	}

	return apiclient.CreateOrUpdateRoleBinding(client, &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapRBACName,
			Namespace: metav1.NamespaceSystem,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbac.GroupName,
			Kind:     "Role",
			Name:     configMapRBACName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind: rbac.GroupKind,
				Name: kubeadmconstants.NodesGroup,
			},
			{
				Kind: rbac.GroupKind,
				Name: kubeadmconstants.NodeBootstrapTokenAuthGroup,
			},
		},
	})
}

func configMapName(kubernetesVersion string) (string, error) {
	version, err := k8sversion.ParseSemantic(kubernetesVersion)
	if err != nil {
		return "", err
	}

	return kubeadmconstants.GetKubeletConfigMapName(version, true), nil
}

func configMapRBACName(kubernetesVersion string) (string, error) {
	version, err := k8sversion.ParseSemantic(kubernetesVersion)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf("%s%d.%d", kubeadmconstants.KubeletBaseConfigMapRolePrefix, version.Major(), version.Minor()), nil
}
