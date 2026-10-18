// Stored videos use the host's freshly signed OSS URL on the ModelArk surface.
// Usage and provider lifecycle projection remain owned by the generation driver.
function projectSeedanceStoredVideo(task, output) {
  const storage = task && task.state && task.state.seedance_storage;
  if (!storage) return output;
  const content = Object.assign({}, output.content || {});
  delete content.video_url;
  if (task.status === "SUCCESS" && storage.video_url) content.video_url = storage.video_url;
  return Object.assign({}, output, {
    content: content,
  });
}

export function extendSeedanceVideoStorage(native) {
  const renderStatus = native.taskStatus;
  const renderList = native.taskList;
  native.taskStatus = function (ctx, task) {
    return projectSeedanceStoredVideo(task, renderStatus(ctx, task));
  };
  native.taskList = function (ctx, page) {
    const result = renderList(ctx, page);
    result.items = result.items.map((item, index) => projectSeedanceStoredVideo(page.tasks[index], item));
    return result;
  };
}
