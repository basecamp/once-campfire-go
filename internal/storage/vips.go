package storage

/*
#cgo pkg-config: vips
#include <vips/vips.h>
#include <stdlib.h>
#include <string.h>

typedef struct { int width; int height; char *error; } campfire_media_result;
static char *campfire_vips_init(void) {
 if (vips_init("once-campfire-go") != 0) return vips_error_buffer_copy();
 vips_block_untrusted_set(TRUE);
 vips_operation_block_set("VipsForeignLoadOpenslide", TRUE);
 return NULL;
}
static campfire_media_result campfire_image(const char *path, const char *output, int width, int height) {
 campfire_media_result result={0};
 VipsImage *image=NULL,*rotated=NULL,*thumbnail=NULL,*mask=NULL,*sharpened=NULL;
 if (output) {
  const char *loader=vips_foreign_find_load(path);
  VipsOperation *operation=loader ? vips_operation_new(loader) : NULL;
  int page=operation && g_object_class_find_property(G_OBJECT_GET_CLASS(operation),"page");
  if (operation) g_object_unref(operation);
  image=page ? vips_image_new_from_file(path,"page",0,NULL) : vips_image_new_from_file(path,NULL);
 } else image=vips_image_new_from_file(path,"access",VIPS_ACCESS_SEQUENTIAL,NULL);
 if (!image) goto failed;
 if (!output) {
  result.width=vips_image_get_width(image);result.height=vips_image_get_height(image);
  if(vips_image_get_typeof(image,"exif-ifd0-Orientation")) {
   char *orientation=NULL;
   if(vips_image_get_as_string(image,"exif-ifd0-Orientation",&orientation)==0) {
    if(strstr(orientation,"Right-top")||strstr(orientation,"Left-bottom")||strstr(orientation,"Top-right")||strstr(orientation,"Bottom-left")){int swap=result.width;result.width=result.height;result.height=swap;}
    g_free(orientation);
   } else vips_error_clear();
  }
  goto done;
 }
 if(vips_autorot(image,&rotated,NULL))goto failed;
 VipsImage *final=rotated;
 if(width||height) {
  if(vips_thumbnail_image(rotated,&thumbnail,width?width:10000000,"height",height?height:10000000,"size",VIPS_SIZE_DOWN,"no_rotate",TRUE,NULL))goto failed;
  double values[]={-1,-1,-1,-1,32,-1,-1,-1,-1};
  mask=vips_image_new_matrix_from_array(3,3,values,9);if(!mask)goto failed;
  vips_image_set_double(mask,"scale",24.0);vips_image_set_double(mask,"offset",0.0);
  if(vips_conv(thumbnail,&sharpened,mask,"precision",VIPS_PRECISION_INTEGER,NULL))goto failed;
  final=sharpened;
 }
 if(vips_image_write_to_file(final,output,NULL))goto failed;
 result.width=vips_image_get_width(final);result.height=vips_image_get_height(final);
 goto done;
failed:
 result.error=vips_error_buffer_copy();vips_error_clear();
done:
 if(sharpened)g_object_unref(sharpened);if(mask)g_object_unref(mask);if(thumbnail)g_object_unref(thumbnail);if(rotated)g_object_unref(rotated);if(image)g_object_unref(image);
 return result;
}
*/
import "C"

import (
	"errors"
	"strings"
	"sync"
	"unsafe"
)

var vipsInit = sync.OnceValue(func() error {
	message := C.campfire_vips_init()
	if message != nil {
		defer C.g_free(C.gpointer(message))
		return errors.New(C.GoString(message))
	}
	return nil
})

func imageProcess(path, output string, width, height int) (int, int, error) {
	if err := vipsInit(); err != nil {
		return 0, 0, err
	}
	if strings.ContainsRune(path, 0) || strings.ContainsRune(output, 0) {
		return 0, 0, errors.New("invalid image path")
	}
	input := C.CString(path)
	defer C.free(unsafe.Pointer(input))
	var out *C.char
	if output != "" {
		out = C.CString(output)
		defer C.free(unsafe.Pointer(out))
	}
	result := C.campfire_image(input, out, C.int(width), C.int(height))
	if result.error != nil {
		defer C.g_free(C.gpointer(result.error))
		return 0, 0, errors.New(C.GoString(result.error))
	}
	return int(result.width), int(result.height), nil
}

func vipsVersion() string { return C.GoString(C.vips_version_string()) }
