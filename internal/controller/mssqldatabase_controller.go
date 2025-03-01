package controller

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	snapshotv1 "github.com/kubernetes-csi/external-snapshotter/client/v4/apis/volumesnapshot/v1"
	corev1 "k8s.io/api/core/v1"

	dbv1 "github.com/JacobBermudes/man-khao/api/v1"
)

// SQLCowReconciler reconciles a SQLCow object
type SQLCowReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

//+kubebuilder:rbac:groups=db.buffalo.com,resources=sqlcows,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=db.buffalo.com,resources=sqlcows/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=core,resources=persistentvolumeclaims,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch
//+kubebuilder:rbac:groups=core,resources=pods/exec,verbs=create
//+kubebuilder:rbac:groups=snapshot.storage.k8s.io,resources=volumesnapshots,verbs=get;list;watch;create;update;patch;delete

func (r *SQLCowReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

	sqlCow := &dbv1.SQLCow{}
	err := r.Get(ctx, req.NamespacedName, sqlCow)
	if err != nil {
		if errors.IsNotFound(err) {
			log.Info("SQLCow resource not found")
			return ctrl.Result{}, nil
		}
		log.Error(err, "Failed to get SQLCow")
		return ctrl.Result{}, err
	}

	if sqlCow.ObjectMeta.Annotations == nil {
		sqlCow.ObjectMeta.Annotations = make(map[string]string)
	}
	if sqlCow.ObjectMeta.Annotations["user"] != sqlCow.Spec.User {
		sqlCow.ObjectMeta.Annotations["user"] = sqlCow.Spec.User
		err = r.Update(ctx, sqlCow)
		if err != nil {
			log.Error(err, "Failed to update SQLCow annotations")
			return ctrl.Result{}, err
		}
		log.Info("Updated annotation with user", "user", sqlCow.Spec.User)
	}

	pod := &corev1.Pod{}
	err = r.Get(ctx, types.NamespacedName{Name: "mssql-0", Namespace: sqlCow.Namespace}, pod)
	if err != nil && errors.IsNotFound(err) {
		if sqlCow.Status.Mounted {
			sqlCow.Status.Mounted = false
			err = r.Status().Update(ctx, sqlCow)
			if err != nil {
				log.Error(err, "Failed to update SQLCow status to unmounted")
				return ctrl.Result{}, err
			}
			log.Info("Pod mssql-0 not found, set mounted to false", "name", sqlCow.Name)
		}
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	} else if err != nil {
		log.Error(err, "Failed to get MSSQL pod")
		return ctrl.Result{}, err
	}

	var basePath string
	if sqlCow.Spec.Template != "" {
		basePath = fmt.Sprintf("/cow/snapshots/%s_%s", sqlCow.Spec.User, sqlCow.Name)
	} else {
		basePath = fmt.Sprintf("/cow/templates/%s_%s", sqlCow.Spec.User, sqlCow.Name)
	}
	mountPath := basePath
	sqlCow.Status.MountPath = mountPath

	var templatePVC string
	var snapshotName string
	if sqlCow.Spec.Template != "" {

		sqlCmd := exec.Command("kubectl", "exec", "mssql-0", "--", "sqlcmd", "-S", "localhost", "-U", "sa", "-P", "KorovaIvanovna12!", "-Q", "SELECT physical_name FROM sys.master_files WHERE database_id = DB_ID('"+sqlCow.Spec.Template+"')")
		output, err := sqlCmd.CombinedOutput()
		if err != nil {
			log.Error(err, "Failed to find database in MSSQL", "output", string(output))
			return ctrl.Result{}, err
		}

		physicalPath := strings.TrimSpace(string(output))
		pvcList := &corev1.PersistentVolumeClaimList{}
		err = r.List(ctx, pvcList, client.InNamespace(sqlCow.Namespace))
		if err != nil {
			log.Error(err, "Failed to list PVCs")
			return ctrl.Result{}, err
		}

		for _, pvc := range pvcList.Items {
			pvName := pvc.Spec.VolumeName
			if pvName != "" && strings.Contains(physicalPath, pvName) {
				templatePVC = pvc.Name
				break
			}
		}

		if templatePVC == "" {
			log.Error(fmt.Errorf("no PVC found for template database: %s", sqlCow.Spec.Template), "Invalid template")
			return ctrl.Result{}, nil
		}

		snapshotName = fmt.Sprintf("mssql-snapshot-%s", sqlCow.Name)
		snapshot := &snapshotv1.VolumeSnapshot{}
		err = r.Get(ctx, types.NamespacedName{Name: snapshotName, Namespace: sqlCow.Namespace}, snapshot)
		if err != nil && errors.IsNotFound(err) {
			snapshot = &snapshotv1.VolumeSnapshot{
				ObjectMeta: metav1.ObjectMeta{
					Name:      snapshotName,
					Namespace: sqlCow.Namespace,
				},
				Spec: snapshotv1.VolumeSnapshotSpec{
					Source: snapshotv1.VolumeSnapshotSource{
						PersistentVolumeClaimName: &templatePVC,
					},
					VolumeSnapshotClassName: func() *string { s := "standard-snapshot-class"; return &s }(),
				},
			}

			err = r.Create(ctx, snapshot)
			if err != nil {
				log.Error(err, "Failed to create VolumeSnapshot")
				return ctrl.Result{}, err
			}

			sqlCow.Status.SnapshotName = snapshotName
			err = r.Status().Update(ctx, sqlCow)
			if err != nil {
				log.Error(err, "Failed to update SQLCow status with snapshot")
				return ctrl.Result{}, err
			}

			log.Info("VolumeSnapshot created", "snapshotName", snapshotName)
			return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
		}
	}

	pvcName := fmt.Sprintf("mssql-copy-%s", sqlCow.Name)
	pvc := &corev1.PersistentVolumeClaim{}
	err = r.Get(ctx, types.NamespacedName{Name: pvcName, Namespace: sqlCow.Namespace}, pvc)
	if err != nil && errors.IsNotFound(err) {
		pvc = &corev1.PersistentVolumeClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      pvcName,
				Namespace: sqlCow.Namespace,
			},
			Spec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
				},
			},
		}

		if sqlCow.Spec.Template != "" {
			pvc.Spec.DataSource = &corev1.TypedLocalObjectReference{
				APIGroup: func() *string { s := "snapshot.storage.k8s.io"; return &s }(),
				Kind:     "VolumeSnapshot",
				Name:     snapshotName,
			}
		}
		pvc.Spec.StorageClassName = func() *string { s := "standard"; return &s }()
		err = r.Create(ctx, pvc)
		if err != nil {
			log.Error(err, "Failed to create PVC")
			return ctrl.Result{}, err
		}
		sqlCow.Status.PVCName = pvcName
		err = r.Status().Update(ctx, sqlCow)
		if err != nil {
			log.Error(err, "Failed to update SQLCow status")
			return ctrl.Result{}, err
		}
		log.Info("PVC created", "pvcName", pvcName)
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	pvName := pvc.Spec.VolumeName
	if pvc.Status.Phase == corev1.ClaimBound {
		checkCmd := exec.Command("kubectl", "exec", "mssql-0", "--", "mountpoint", "-q", mountPath)
		if err := checkCmd.Run(); err != nil {
			log.Info("Volume not mounted, remounting", "mountPath", mountPath)
			mkdirCmd := exec.Command("kubectl", "exec", "mssql-0", "--", "mkdir", "-p", mountPath)
			if output, err := mkdirCmd.CombinedOutput(); err != nil {
				log.Error(err, "Failed to create directory", "output", string(output))
				return ctrl.Result{}, err
			}

			mountCmd := exec.Command("kubectl", "exec", "mssql-0", "--", "mount", fmt.Sprintf("/dev/%s", pvName), mountPath)
			output, err := mountCmd.CombinedOutput()
			if err != nil {
				log.Error(err, "Failed to mount volume", "output", string(output))
				return ctrl.Result{}, err
			}
			sqlCow.Status.Mounted = true
		} else if !sqlCow.Status.Mounted {
			sqlCow.Status.Mounted = true
			log.Info("Volume already mounted, updating status", "mountPath", mountPath)
		}
		err = r.Status().Update(ctx, sqlCow)
		if err != nil {
			log.Error(err, "Failed to update SQLCow status")
			return ctrl.Result{}, err
		}
	}

	if !sqlCow.Status.DatabaseCreated && sqlCow.Status.Mounted {
		var sqlQuery string
		if sqlCow.Spec.Template != "" {
			// snap
			sqlQuery = fmt.Sprintf("CREATE DATABASE %s ON (NAME = '%s_data', FILENAME = '%s/data/db.mdf') LOG ON (NAME = '%s_log', FILENAME = '%s/data/db.ldf') FOR ATTACH",
				sqlCow.Spec.DatabaseName, sqlCow.Spec.DatabaseName, mountPath, sqlCow.Spec.DatabaseName, mountPath)
		} else {
			// template
			sqlQuery = fmt.Sprintf("CREATE DATABASE %s ON (NAME = '%s_data', FILENAME = '%s/%s_data.mdf') LOG ON (NAME = '%s_log', FILENAME = '%s/%s_log.ldf')",
				sqlCow.Spec.DatabaseName, sqlCow.Spec.DatabaseName, mountPath, sqlCow.Spec.DatabaseName, sqlCow.Spec.DatabaseName, mountPath)
		}
		sqlCmd := exec.Command("kubectl", "exec", "mssql-0", "--", "sqlcmd", "-S", "localhost", "-U", "sa", "-P", "KorovaIvanovna12!", "-Q", sqlQuery, "-No")
		output, err := sqlCmd.CombinedOutput()
		if err != nil {
			log.Error(err, "Failed to create/attach database", "output", string(output))
			return ctrl.Result{}, err
		}
		sqlCow.Status.DatabaseCreated = true
		err = r.Status().Update(ctx, sqlCow)
		if err != nil {
			log.Error(err, "Failed to update SQLCow status")
			return ctrl.Result{}, err
		}
		log.Info("Database created", "databaseName", sqlCow.Spec.DatabaseName)
	}

	return ctrl.Result{}, nil
}

func (r *SQLCowReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1.SQLCow{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&snapshotv1.VolumeSnapshot{}).
		Complete(r)
}
