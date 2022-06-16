/*
  Copyright 2022 The Go Authors. All rights reserved.
  Use of this source code is governed by a BSD-style
  license that can be found in the LICENSE file.
*/

(function () {
  /**
   * registerTaskListExpandListeners toggles displaying of logs on workflow
   * tasks.
   *
   * For each selector on the page, add a TaskList-Expanded class to this
   * element and its next sibling.
   *
   * @param {string} selector - css selector for target element
   */
  const registerTaskListExpandListeners = (selector) => {
    document.querySelectorAll(selector).forEach((element) => {
      element.addEventListener("click", (e) => {
        e.stopPropagation();
        element.classList.toggle("TaskList-expanded");
        element.nextElementSibling.classList.toggle("TaskList-expanded");
      });
    });
  };

  /**
   * addSliceRow creates and appends a row to a slice parameter
   * for filling in an element.
   *
   * @param {HTMLElement} container - the container element
   * @param {string} paramName - the parameter name
   * @param {string} element - the element tag to create
   * @param {string} inputType - the type attribute if element is "input"
   * @param {string} paramExample - an example value for the parameter
   */
  const addSliceRow = (container, paramName, element, inputType, paramExample) => {
    /*
      Create an input element, a button to remove it, group them in a "parameterRow" div:

      <div class="NewWorkflow-parameterRow">
        <input name="workflow.params.{{$p.Name}}" placeholder="{{paramExample}}" />
        <button title="Remove this row from the slice." onclick="/ * Remove this row. * /">-</button>
      </div>
    */
    const input = document.createElement(element);
    input.name = "workflow.params." + paramName;
    if (element === "input") {
      input.type = inputType;
    }
    input.placeholder = paramExample;
    const removeButton = document.createElement("button");
    removeButton.title = "Remove this row from the slice.";
    removeButton.addEventListener("click", (e) => {
      e.preventDefault();
      container.removeChild(div);
    });
    removeButton.appendChild(document.createTextNode("-"));
    const div = document.createElement("div");
    div.className = "NewWorkflow-parameterRow";
    div.appendChild(input);
    div.appendChild(removeButton);

    // Add the "parameterRow" div to the document.
    container.appendChild(div);
  };

  /** addSliceRowListener registers listeners for addSliceRow.
   *
   * @param {string} selector - elements to add click listener for addSliceRow.
   */
  const addSliceRowListener = (selector) => {
    document.querySelectorAll(selector).forEach((element) => {
      element.addEventListener("click", (e) => {
        e.stopPropagation();
        addSliceRow(
          element.parentElement.parentElement,
          element.dataset.paramname,
          element.dataset.element,
          element.dataset.inputtype,
          element.dataset.paramexample
        );
      });
    });
  };

  const registerListeners = () => {
    registerTaskListExpandListeners(".TaskList-expandableItem");
    addSliceRowListener(".NewWorkflow-addSliceRowButton");
  };
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", registerListeners);
  } else {
    registerListeners();
  }
})();
