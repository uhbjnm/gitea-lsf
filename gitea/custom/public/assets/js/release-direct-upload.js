(() => {
  // Verified against Gitea 1.26.2 and @deltablot/dropzone 7.4.4.
  const page = document.querySelector('.page-content.repository.new.release');
  if (!page) return;

  const dropzoneElement = page.querySelector('.dropzone');
  if (!dropzoneElement) return;

  const form = dropzoneElement.closest('form');
  const uploadURL = `${dropzoneElement.dataset.uploadUrl}/direct`;
  let activeUploads = 0;
  let attempts = 0;

  const requestJSON = async (url, body) => {
    const response = await fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
        'X-Csrf-Token': window.config?.csrfToken || '',
      },
      body: JSON.stringify(body),
    });
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.message || response.statusText);
    return data;
  };

  const setFormBusy = (busy) => {
    const wasActive = activeUploads > 0;
    activeUploads += busy ? 1 : -1;
    if (activeUploads < 0) activeUploads = 0;
    for (const button of form.querySelectorAll('button[type="submit"], button:not([type])')) {
      if (!wasActive && activeUploads > 0) {
        button.dataset.releaseUploadWasDisabled = String(button.disabled);
        button.disabled = true;
      } else if (button.dataset.releaseUploadWasDisabled !== undefined) {
        button.disabled = button.dataset.releaseUploadWasDisabled === 'true';
        delete button.dataset.releaseUploadWasDisabled;
      }
    }
  };

  const putFile = (dropzone, file, upload) => new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    file.xhr = xhr;
    xhr.open('PUT', upload.href, true);
    for (const [name, value] of Object.entries(upload.header || {})) xhr.setRequestHeader(name, value);
    xhr.upload.addEventListener('progress', (event) => {
      if (!event.lengthComputable) return;
      dropzone.emit('uploadprogress', file, event.loaded / event.total * 100, event.loaded);
    });
    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) resolve();
      else reject(new Error(`OSS upload failed: HTTP ${xhr.status}`));
    });
    xhr.addEventListener('error', () => reject(new Error('OSS upload failed')));
    xhr.addEventListener('abort', () => reject(new Error('Upload canceled')));
    xhr.send(file);
  });

  const uploadFile = async (dropzone, file) => {
    setFormBusy(true);
    try {
      const init = await requestJSON(uploadURL, {name: file.name, size: file.size});
      await putFile(dropzone, file, init.upload);
      if (file.status === 'canceled') return;
      const completed = await requestJSON(init.complete_url, {token: init.token});
      dropzone._finished([file], completed, null);
    } catch (error) {
      if (file.status !== 'canceled') {
        dropzone._errorProcessing([file], error instanceof Error ? error.message : String(error), file.xhr);
      }
    } finally {
      setFormBusy(false);
    }
  };

  const patchDropzone = () => {
    const dropzone = dropzoneElement.dropzone;
    if (!dropzone) {
      if (++attempts < 200) setTimeout(patchDropzone, 50);
      return;
    }
    if (typeof dropzone._finished !== 'function' || typeof dropzone._errorProcessing !== 'function') {
      console.error('Release direct upload is incompatible with this Gitea Dropzone version');
      return;
    }
    dropzone.uploadFiles = (files) => {
      for (const file of files) void uploadFile(dropzone, file);
    };
    dropzoneElement.dataset.releaseDirectUploadReady = 'true';
  };

  patchDropzone();
})();
