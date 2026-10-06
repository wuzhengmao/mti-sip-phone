// 本文件与同目录的 dshow_windows.cpp 复制自 github.com/pion/mediadevices
// v0.10.0 的 pkg/driver/camera/camera_windows.{hpp,cpp}
// （MIT License, Copyright (c) 2018-2020 Pion）。
//
// 与上游的差异只有一处：给 SampleGrabberCallback 的 SampleCB / BufferCB
// 补上 **STDMETHODCALLTYPE**。上游漏写这个调用约定标记，mingw 的 qedit.h 里
// ISampleGrabberCB::SampleCB 带 __stdcall —— x86_64 上 __stdcall 被忽略所以
// 能编能跑，**i686（windows-386）上是真实的调用约定冲突**，编译期直接报错。
// 补上之后两个架构都走这一份实现（amd64 不再用上游那份）。
// 详见 CLAUDE.md 的「已知陷阱 · windows-386 摄像头」条。

#pragma once

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct
{
  int width;
  int height;
  uint32_t fcc;
} imageProp;

typedef struct
{
  int width;
  int height;
  uint32_t fcc;
  size_t buf;  // uintptr

  char* name;
  int numProps;
  imageProp* props;

  void* grabber;
  void* mediaControl;
  void* callback;
} camera;

typedef struct
{
  int num;
  char** name;
  char** friendlyName;
} cameraList;

int openCamera(camera* cam, const char** errstr);
void freeCamera(camera* cam);
int listResolution(camera* cam, const char** errstr);
int listCamera(cameraList* list, const char** errstr);
int freeCameraList(cameraList* list, const char** errstr);

imageProp* getProp(camera* cam, int i);

char* getName(cameraList* list, int i);
char* getFriendlyName(cameraList* list, int i);

#ifdef __cplusplus
}
#endif

#ifdef __cplusplus
#include <windows.h>
#include <string>
#include <dshow.h>
std::string utf16Decode(LPOLESTR olestr);
IPin* getPin(IBaseFilter* filter, PIN_DIRECTION dir);
char* getCameraName(IMoniker* monier);

template <class T>
void safeRelease(T** p)
{
  if (*p)
  {
    (*p)->Release();
    *p = nullptr;
  }
}

class SampleGrabberCallback : public ISampleGrabberCB
{
private:
  camera* cam_;

public:
  inline SampleGrabberCallback(camera* cam)
    : cam_(cam)
  {
  }

  HRESULT STDMETHODCALLTYPE SampleCB(double sampleTime, IMediaSample* sample) final;
  HRESULT STDMETHODCALLTYPE BufferCB(double sampleTime, BYTE* buffer, LONG bufferLen) final;

  inline ULONG STDMETHODCALLTYPE AddRef() final
  {
    return 2;
  }
  inline ULONG STDMETHODCALLTYPE Release() final
  {
    return 1;
  }
  inline HRESULT STDMETHODCALLTYPE QueryInterface(REFIID riid, void** ppv) final
  {
    if (riid == IID_ISampleGrabberCB || riid == IID_IUnknown)
    {
      *ppv = (void*)static_cast<ISampleGrabberCB*>(this);
      return NOERROR;
    }
    return E_NOINTERFACE;
  }
};

EXTERN_C const CLSID CLSID_NullRenderer;
EXTERN_C const CLSID CLSID_SampleGrabber;

const static char* errAddFilter = "failed to add filter";
const static char* errConnectFilters = "failed to connect filters";
const static char* errEnumDevice = "failed to enum device";
const static char* errGrabber = "failed to create grabber";
const static char* errGraphBuilder = "failed to build graph";
const static char* errNoControl = "failed to control media";
const static char* errTerminator = "failed to create graph terminator";
const static char* errGetConfig = "failed to get config";

#endif
