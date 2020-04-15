/*
 *
 * Copyright © 2020 Dell Inc. or its subsidiaries. All Rights Reserved.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package gobrick

import (
	"context"
	"github.com/dell/gobrick/internal/logger"
	intmultipath "github.com/dell/gobrick/internal/multipath"
	intscsi "github.com/dell/gobrick/internal/scsi"
	"github.com/dell/gobrick/internal/tracer"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

const (
	multipathFlushTimeoutDefault = time.Second * 120
	deviceMapperPrefix           = "dm-"
)

type baseConnectorParams struct {
	MultipathFlushTimeout time.Duration
}

func newBaseConnector(mp intmultipath.Multipath, s intscsi.SCSI, params baseConnectorParams) *baseConnector {
	conn := &baseConnector{
		multipath: mp,
		scsi:      s,
	}
	setTimeouts(&conn.multipathFlushTimeout,
		params.MultipathFlushTimeout, multipathFlushTimeoutDefault)

	return conn
}

type baseConnector struct {
	multipath intmultipath.Multipath
	scsi      intscsi.SCSI

	multipathFlushTimeout time.Duration
}

func (bc *baseConnector) disconnectDevicesByDeviceName(ctx context.Context, name string) error {
	defer tracer.TraceFuncCall(ctx, "baseConnector.disconnectDevicesByDeviceName")()
	if !bc.scsi.IsDeviceExist(ctx, name) {
		logger.Info(ctx, "device %s not found", name)
		return nil
	}
	var err error
	var wwn string
	if strings.HasPrefix(name, deviceMapperPrefix) {
		wwn, err = bc.getDMWWN(ctx, name)

	} else {
		wwn, err = bc.scsi.GetDeviceWWN(ctx, []string{name})
	}
	if err != nil {
		logger.Error(ctx, "can't find wwn for device: %s", err.Error())
		return err
	}

	devices, err := bc.scsi.GetDevicesByWWN(ctx, wwn)
	if err != nil {
		logger.Error(ctx, "failed to find devices by wwn: %s", err.Error())
		return err
	}
	return bc.cleanDevices(ctx, false, devices)
}

func (bc *baseConnector) cleanDevices(ctx context.Context,
	force bool, devices []string) error {
	defer tracer.TraceFuncCall(ctx, "baseConnector.cleanDevices")()
	dm, err := bc.scsi.GetDMDeviceByChildren(ctx, devices)
	if err != nil {
		logger.Info(ctx, "multipath device not found: %s", err.Error())
	} else {
		err := bc.cleanMultipathDevice(ctx, dm)
		if err != nil {
			msg := fmt.Sprintf("failed to flush multipath device: %s", err.Error())
			logger.Error(ctx, msg)
			if !force {
				return err
			}
		}
	}
	for _, d := range devices {
		err := bc.scsi.DeleteSCSIDeviceByName(ctx, d)
		if err != nil {
			logger.Error(ctx, "can't delete block device: %s", err.Error())
			if !force {
				return err
			}
		}
		if dm != "" {
			_ = bc.multipath.DelPath(ctx, path.Join("/dev", d))
		}
	}
	return nil
}

func (bc *baseConnector) cleanMultipathDevice(ctx context.Context, dm string) error {
	defer tracer.TraceFuncCall(ctx, "baseConnector.cleanMultipathDevice")()
	ctx, cancelFunc := context.WithTimeout(ctx, bc.multipathFlushTimeout)
	defer cancelFunc()
	err := bc.multipath.FlushDevice(ctx, path.Join("/dev/", dm))
	if err != nil {
		return err
	}
	return nil
}

func (bc *baseConnector) getDMWWN(ctx context.Context, dm string) (string, error) {
	defer tracer.TraceFuncCall(ctx, "baseConnector.getDMWWN")()
	logger.Info(ctx, "resolve wwn for DM: %s", dm)
	children, err := bc.scsi.GetDMChildren(ctx, dm)
	if err == nil {
		logger.Debug(ctx, "children for DM %s: %s", dm, children)
		wwn, err := bc.scsi.GetDeviceWWN(ctx, children)
		if err != nil {
			logger.Error(ctx, "failed to read WWN for DM %s children: %s", dm, err.Error())
			return "", err
		}
		logger.Debug(ctx, "WWN for DM %s is: %s", dm, wwn)
		return wwn, nil
	}
	logger.Debug(ctx, "failed to get children for DM %s: %s", dm, err.Error())
	logger.Info(ctx, "can't resolve DM %s WWN from children devices, query multipathd", dm)
	wwn, err := bc.multipath.GetDMWWID(ctx, dm)
	if err != nil {
		msg := fmt.Sprintf("failed to resolve DM %s WWN: %s", dm, err.Error())
		logger.Error(ctx, msg)
		return "", errors.New(msg)
	}
	logger.Info(ctx, "WWN for DM %s is: %s", dm, wwn)
	return wwn, nil
}
