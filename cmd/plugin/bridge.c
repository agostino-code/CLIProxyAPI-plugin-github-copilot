//go:build cgo && (linux || darwin || freebsd || windows)

#include "bridge.h"
#include <string.h>
#if defined(__APPLE__) && defined(__x86_64__)
#include <pthread.h>
#define ISOLATE_GO_RUNTIMES 1
#else
#define ISOLATE_GO_RUNTIMES 0
#endif
/* A PE DLL exports nothing by default once any symbol uses dllexport, which
 * Go's own //export stubs do, so the entry point must opt in explicitly. */
#if defined(_WIN32)
#define CLIPROXY_EXPORT __declspec(dllexport)
#else
#define CLIPROXY_EXPORT __attribute__((visibility("default")))
#endif

extern int cliproxyGoInit(void);
extern int cliproxyGoCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyGoShutdown(void);
static cliproxy_host_api host_api;

/* Darwin/amd64 Go runtimes use thread-local state which must not be shared
 * between a Go executable and a Go c-shared library (Go issue #38692).
 * Enter each runtime on a fresh native thread, in BOTH callback directions.
 * Other supported targets keep the direct, lower-overhead ABI path.
 * Buffers stay alive until pthread_join returns; no Go pointers cross C. */
static int invoke(void* (*function)(void*), void* arg) {
#if ISOLATE_GO_RUNTIMES
    pthread_t thread;
    if (pthread_create(&thread, NULL, function, arg) != 0) return 1;
    if (pthread_join(thread, NULL) != 0) abort(); /* Cannot outlive stack-owned args. */
#else
    function(arg);
#endif
    return 0;
}

typedef struct {
    const char* method;
    const uint8_t* request;
    size_t length;
    cliproxy_buffer* response;
    int result;
} call_task;

static void* plugin_call_body(void* arg) {
    call_task* task = arg;
    task->result = cliproxyGoCall((char*)task->method, (uint8_t*)task->request, task->length, task->response);
    return NULL;
}
static int plugin_call(const char* method, const uint8_t* request, size_t length, cliproxy_buffer* response) {
    call_task task = {method, request, length, response, 1};
    if (invoke(plugin_call_body, &task) != 0) return 1;
    return task.result;
}
static void plugin_free(void* ptr, size_t length) { (void)length; free(ptr); }
static void* plugin_init_body(void* arg) { *(int*)arg = cliproxyGoInit(); return NULL; }
static void* plugin_shutdown_body(void* arg) { (void)arg; cliproxyGoShutdown(); return NULL; }
static void plugin_shutdown(void) {
    /* Shutdown must complete before the host can unload the library. */
    if (invoke(plugin_shutdown_body, NULL) != 0) abort();
}

CLIPROXY_EXPORT
int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
    if (!host || !plugin || host->abi_version != 1 || !host->call || !host->free_buffer) return 1;
    host_api = *host;
    int result = 1;
    if (invoke(plugin_init_body, &result) != 0 || result != 0) return 1;
    plugin->abi_version = 1;
    plugin->call = plugin_call;
    plugin->free_buffer = plugin_free;
    plugin->shutdown = plugin_shutdown;
    return 0;
}

static void* host_call_body(void* arg) {
    call_task* task = arg;
#if ISOLATE_GO_RUNTIMES
    cliproxy_buffer source = {NULL, 0};
    task->result = host_api.call(host_api.host_ctx, task->method, task->request, task->length, &source);
    task->response->ptr = NULL;
    task->response->len = 0;
    if (source.ptr && source.len > 0 && source.len <= 64u * 1024u * 1024u) {
        void* copy = malloc(source.len);
        if (copy) {
            memcpy(copy, source.ptr, source.len);
            task->response->ptr = copy;
            task->response->len = source.len;
        } else { task->result = 1; }
    } else { task->result = 1; }
    /* The host's free callback also enters its Go runtime: keep it on this thread. */
    if (source.ptr) host_api.free_buffer(source.ptr, source.len);
#else
    task->result = host_api.call(host_api.host_ctx, task->method, task->request, task->length, task->response);
#endif
    return NULL;
}
int call_host_api(const char* method, const uint8_t* request, size_t length, cliproxy_buffer* response) {
    if (!host_api.call || !response) return 1;
    call_task task = {method, request, length, response, 1};
    if (invoke(host_call_body, &task) != 0) return 1;
    return task.result;
}
void free_host_buffer(void* ptr, size_t length) {
    if (!ptr) return;
#if ISOLATE_GO_RUNTIMES
    (void)length;
    free(ptr);
#else
    host_api.free_buffer(ptr, length);
#endif
}
