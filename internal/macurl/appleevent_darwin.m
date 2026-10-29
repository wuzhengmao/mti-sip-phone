#include <Cocoa/Cocoa.h>
#include <CoreServices/CoreServices.h>
#include <stdlib.h>

// Go 侧导出（见 url_darwin.go 的 //export mtiGoHandleURL）。
extern void mtiGoHandleURL(char *url);

// MTIURLAppleEventHandler 接收 LaunchServices 投递的 kAEGetURL 事件。
// 这里手动 declare 而不 include cgo 生成的 _cgo_export.h：单独编译单元里
// 自己声明 extern 更省事，C 链接名与 cgo 生成的符号一致。
@interface MTIURLAppleEventHandler : NSObject
- (void)handleGetURLEvent:(NSAppleEventDescriptor *)event
           withReplyEvent:(NSAppleEventDescriptor *)replyEvent;
@end

@implementation MTIURLAppleEventHandler
- (void)handleGetURLEvent:(NSAppleEventDescriptor *)event
           withReplyEvent:(NSAppleEventDescriptor *)replyEvent {
    NSAppleEventDescriptor *desc = [event paramDescriptorForKeyword:keyDirectObject];
    NSString *url = [desc stringValue];
    if (url == nil) {
        return;
    }
    const char *c = [url UTF8String];
    if (c != NULL) {
        mtiGoHandleURL((char *)c);
    }
}
@end

static MTIURLAppleEventHandler *g_handler = nil;

void mtiInstallAppleEventHandler(void) {
    if (g_handler != nil) {
        return;
    }
    g_handler = [[MTIURLAppleEventHandler alloc] init];
    [[NSAppleEventManager sharedAppleEventManager]
        setEventHandler:g_handler
            andSelector:@selector(handleGetURLEvent:withReplyEvent:)
          forEventClass:kInternetEventClass
             andEventID:kAEGetURL];
}
