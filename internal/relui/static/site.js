/*
  Copyright 2022 The Go Authors. All rights reserved.
  Use of this source code is governed by a BSD-style
  license that can be found in the LICENSE file.
*/

;(function () {
  const registerTaskListExpandListeners = (selector) => {
    document.querySelectorAll(selector).forEach((element) => {
      element.addEventListener("click", (e) => {
        e.stopPropagation()
        element.classList.toggle("TaskList-expanded")
        element.nextElementSibling.classList.toggle("TaskList-expanded")
      })
    })
  }

  registerTaskListExpandListeners(".TaskList-expandableItem")
})()

// addSliceRow creates and appends a row to a slice parameter
// for filling in an element.
//
// container is the parameter container element.
// paramName is the parameter name.
// element is the element tag to create, and inputType is its type attribute if element is "input".
// paramExample is an example value for the parameter.
addSliceRow = (container, paramName, element, inputType, paramExample) => {
  /*
    Create an input element, a button to remove it, group them in a "parameterRow" div:

    <div class="NewWorkflow-parameterRow">
      <input name="workflow.params.{{$p.Name}}" placeholder="{{paramExample}}" />
      <button title="Remove this row from the slice." onclick="/ * Remove this row. * /">-</button>
    </div>
  */
  const input = document.createElement(element)
  input.name = "workflow.params." + paramName
  if (element == "input") {
    input.type = inputType
  }
  input.placeholder = paramExample
  const removeButton = document.createElement("button")
  removeButton.title = "Remove this row from the slice."
  removeButton.addEventListener("click", (e) => {
    e.preventDefault()
    container.removeChild(div)
  })
  removeButton.appendChild(document.createTextNode("-"))
  const div = document.createElement("div")
  div.className = "NewWorkflow-parameterRow";
  div.appendChild(input)
  div.appendChild(removeButton)

  // Add the "parameterRow" div to the document.
  container.appendChild(div)
}
