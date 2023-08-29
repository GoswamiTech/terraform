// Code generated by MockGen. DO NOT EDIT.
// Source: plan_export.go

// Package mocks is a generated GoMock package.
package mocks

import (
	context "context"
	reflect "reflect"

	gomock "github.com/golang/mock/gomock"
	tfe "github.com/hashicorp/go-tfe"
)

// MockPlanExports is a mock of PlanExports interface.
type MockPlanExports struct {
	ctrl     *gomock.Controller
	recorder *MockPlanExportsMockRecorder
}

// MockPlanExportsMockRecorder is the mock recorder for MockPlanExports.
type MockPlanExportsMockRecorder struct {
	mock *MockPlanExports
}

// NewMockPlanExports creates a new mock instance.
func NewMockPlanExports(ctrl *gomock.Controller) *MockPlanExports {
	mock := &MockPlanExports{ctrl: ctrl}
	mock.recorder = &MockPlanExportsMockRecorder{mock}
	return mock
}

// EXPECT returns an object that allows the caller to indicate expected use.
func (m *MockPlanExports) EXPECT() *MockPlanExportsMockRecorder {
	return m.recorder
}

// Create mocks base method.
func (m *MockPlanExports) Create(ctx context.Context, options tfe.PlanExportCreateOptions) (*tfe.PlanExport, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", ctx, options)
	ret0, _ := ret[0].(*tfe.PlanExport)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Create indicates an expected call of Create.
func (mr *MockPlanExportsMockRecorder) Create(ctx, options interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*MockPlanExports)(nil).Create), ctx, options)
}

// Delete mocks base method.
func (m *MockPlanExports) Delete(ctx context.Context, planExportID string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", ctx, planExportID)
	ret0, _ := ret[0].(error)
	return ret0
}

// Delete indicates an expected call of Delete.
func (mr *MockPlanExportsMockRecorder) Delete(ctx, planExportID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockPlanExports)(nil).Delete), ctx, planExportID)
}

// Download mocks base method.
func (m *MockPlanExports) Download(ctx context.Context, planExportID string) ([]byte, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Download", ctx, planExportID)
	ret0, _ := ret[0].([]byte)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Download indicates an expected call of Download.
func (mr *MockPlanExportsMockRecorder) Download(ctx, planExportID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Download", reflect.TypeOf((*MockPlanExports)(nil).Download), ctx, planExportID)
}

// Read mocks base method.
func (m *MockPlanExports) Read(ctx context.Context, planExportID string) (*tfe.PlanExport, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Read", ctx, planExportID)
	ret0, _ := ret[0].(*tfe.PlanExport)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Read indicates an expected call of Read.
func (mr *MockPlanExportsMockRecorder) Read(ctx, planExportID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Read", reflect.TypeOf((*MockPlanExports)(nil).Read), ctx, planExportID)
}
